// SPDX-License-Identifier: AGPL-3.0-only

package indexheader

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/oklog/ulid"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
	"github.com/thanos-io/objstore/providers/filesystem"
	"golang.org/x/exp/slices"

	"github.com/grafana/mimir/pkg/storage/tsdb/block"
	"github.com/grafana/mimir/pkg/storage/tsdb/metadata"
	"github.com/grafana/mimir/pkg/storegateway/testhelper"
	"github.com/grafana/mimir/pkg/util/test"
)

func BenchmarkLookupSymbol(b *testing.B) {
	ctx := context.Background()

	bucketDir := b.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(bucketDir, "bkt"))
	require.NoError(b, err)
	b.Cleanup(func() {
		require.NoError(b, bkt.Close())
	})

	// TODO: are the number of name and value symbols representative?
	nameSymbols := generateSymbols("name", 20)
	valueSymbols := generateSymbols("value", 1000)
	idIndexV2, err := testhelper.CreateBlock(ctx, bucketDir, generateLabels(nameSymbols, valueSymbols), 100, 0, 1000, labels.FromStrings("ext1", "1"), 124)
	require.NoError(b, err)
	require.NoError(b, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(bucketDir, idIndexV2.String()), nil))

	indexName := filepath.Join(bucketDir, idIndexV2.String(), block.IndexHeaderFilename)
	require.NoError(b, WriteBinary(ctx, bkt, idIndexV2, indexName))

	// TODO: are these sensible values for parallelism?
	for _, parallelism := range []int{1, 2, 4, 8, 20, 100} {
		// TODO: are these sensible value for name lookup percentage?
		for _, percentageNameLookups := range []int{20, 40, 50, 60, 80} {
			b.Run(fmt.Sprintf("NameLookups%v%%-Parallelism%v", percentageNameLookups, parallelism), func(b *testing.B) {
				benchmarkLookupSymbol(ctx, b, bucketDir, idIndexV2, parallelism, percentageNameLookups, nameSymbols, valueSymbols)
			})
		}
	}
}

func benchmarkLookupSymbol(ctx context.Context, b *testing.B, bucketDir string, id ulid.ULID, parallelism int, percentageNameLookups int, nameSymbols []string, valueSymbols []string) {
	br, err := NewStreamBinaryReader(ctx, log.NewNopLogger(), nil, bucketDir, id, 32, NewStreamBinaryReaderMetrics(nil), Config{})
	require.NoError(b, err)
	b.Cleanup(func() {
		require.NoError(b, br.Close())
	})

	nameIndices, nameMap := reverseLookup(b, br, nameSymbols)
	valueIndices, valueMap := reverseLookup(b, br, valueSymbols)

	b.SetParallelism(parallelism)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		// Use our own random source to avoid contention for the global random number generator.
		random := rand.New(rand.NewSource(time.Now().UnixNano()))
		count := random.Int()

		for pb.Next() {
			var indices []uint32
			var indicesToSymbol map[uint32]string

			if count%100 < percentageNameLookups {
				indices = nameIndices
				indicesToSymbol = nameMap
			} else {
				indices = valueIndices
				indicesToSymbol = valueMap
			}

			index := indices[random.Intn(len(indices))]
			expectedSymbol := indicesToSymbol[index]
			actualSymbol, err := br.LookupSymbol(index)

			// Why do we wrap require.NoError or require.Equal in an if block here? These methods perform some synchronisation
			// that ends up dominating the benchmark, so we only want to call them if they're needed.
			if err != nil {
				require.NoError(b, err)
			}

			if actualSymbol != expectedSymbol {
				require.Equal(b, expectedSymbol, actualSymbol)
			}

			count++
		}
	})
}

func BenchmarkLabelNames(b *testing.B) {
	ctx := context.Background()

	bucketDir := b.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(bucketDir, "bkt"))
	require.NoError(b, err)
	b.Cleanup(func() {
		require.NoError(b, bkt.Close())
	})

	for _, nameCount := range []int{20, 50, 100, 200} {
		for _, valueCount := range []int{100, 500, 1000} {
			nameSymbols := generateSymbols("name", nameCount)
			valueSymbols := generateSymbols("value", valueCount)
			idIndexV2, err := testhelper.CreateBlock(ctx, bucketDir, generateLabels(nameSymbols, valueSymbols), 100, 0, 1000, labels.FromStrings("ext1", "1"), 124)
			require.NoError(b, err)
			require.NoError(b, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(bucketDir, idIndexV2.String()), nil))

			indexName := filepath.Join(bucketDir, idIndexV2.String(), block.IndexHeaderFilename)
			require.NoError(b, WriteBinary(ctx, bkt, idIndexV2, indexName))

			b.Run(fmt.Sprintf("%vNames%vValues", nameCount, valueCount), func(b *testing.B) {
				benchmarkReaders(b, bucketDir, idIndexV2, func(b *testing.B, br Reader) {
					slices.Sort(nameSymbols)
					b.ResetTimer()

					for i := 0; i < b.N; i++ {
						actualNames, err := br.LabelNames()

						require.NoError(b, err)
						require.Equal(b, nameSymbols, actualNames)
					}
				})
			})
		}
	}
}

func BenchmarkLabelValuesIndexV1(b *testing.B) {
	ctx := context.Background()

	bucketDir := b.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(bucketDir, "bkt"))
	require.NoError(b, err)
	b.Cleanup(func() {
		require.NoError(b, bkt.Close())
	})

	metaIndexV1, err := metadata.ReadFromDir("./testdata/index_format_v1")
	require.NoError(b, err)
	test.Copy(b, "./testdata/index_format_v1", filepath.Join(bucketDir, metaIndexV1.ULID.String()))

	_, err = metadata.InjectThanos(log.NewNopLogger(), filepath.Join(bucketDir, metaIndexV1.ULID.String()), metadata.Thanos{
		Labels:     labels.FromStrings("ext1", "1").Map(),
		Downsample: metadata.ThanosDownsample{Resolution: 0},
		Source:     metadata.TestSource,
	}, &metaIndexV1.BlockMeta)

	require.NoError(b, err)
	require.NoError(b, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(bucketDir, metaIndexV1.ULID.String()), nil))

	indexName := filepath.Join(bucketDir, metaIndexV1.ULID.String(), block.IndexHeaderFilename)
	require.NoError(b, WriteBinary(ctx, bkt, metaIndexV1.ULID, indexName))

	benchmarkReaders(b, bucketDir, metaIndexV1.ULID, func(b *testing.B, br Reader) {
		names, err := br.LabelNames()
		require.NoError(b, err)

		rand.Shuffle(len(names), func(i, j int) {
			names[i], names[j] = names[j], names[i]
		})

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			name := names[i%len(names)]

			values, err := br.LabelValues(name, func(s string) bool {
				return true
			})

			require.NoError(b, err)
			require.NotEmpty(b, values)
		}
	})
}

func benchmarkReaders(b *testing.B, bucketDir string, id ulid.ULID, benchmark func(b *testing.B, br Reader)) {
	b.Run("StreamBinaryReader", func(b *testing.B) {
		br, err := NewStreamBinaryReader(context.Background(), log.NewNopLogger(), nil, bucketDir, id, 32, NewStreamBinaryReaderMetrics(nil), Config{})
		require.NoError(b, err)
		b.Cleanup(func() {
			require.NoError(b, br.Close())
		})

		benchmark(b, br)
	})

	b.Run("BinaryReader", func(b *testing.B) {
		br, err := NewBinaryReader(context.Background(), log.NewNopLogger(), nil, bucketDir, id, 32, Config{})
		require.NoError(b, err)
		b.Cleanup(func() {
			require.NoError(b, br.Close())
		})

		benchmark(b, br)
	})
}

func BenchmarkNewStreamBinaryReader(b *testing.B) {
	ctx := context.Background()

	bucketDir := b.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(bucketDir, "bkt"))
	require.NoError(b, err)
	b.Cleanup(func() {
		require.NoError(b, bkt.Close())
	})

	for _, nameCount := range []int{1, 20, 50, 100, 200} {
		for _, valueCount := range []int{1, 10, 100, 500, 1000, 5000} {
			nameSymbols := generateSymbols("name", nameCount)
			valueSymbols := generateSymbols("value", valueCount)
			idIndexV2, err := testhelper.CreateBlock(ctx, bucketDir, generateLabels(nameSymbols, valueSymbols), 100, 0, 1000, labels.FromStrings("ext1", "1"), 124)
			require.NoError(b, err)
			require.NoError(b, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(bucketDir, idIndexV2.String()), nil))

			indexName := filepath.Join(bucketDir, idIndexV2.String(), block.IndexHeaderFilename)
			require.NoError(b, WriteBinary(ctx, bkt, idIndexV2, indexName))

			b.Run(fmt.Sprintf("%vNames%vValues", nameCount, valueCount), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					br, err := NewStreamBinaryReader(ctx, log.NewNopLogger(), nil, bucketDir, idIndexV2, 32, NewStreamBinaryReaderMetrics(nil), Config{})
					require.NoError(b, err)
					b.Cleanup(func() {
						require.NoError(b, br.Close())
					})
				}
			})
		}
	}
}

func generateSymbols(prefix string, count int) []string {
	s := make([]string, 0, count)

	for idx := 0; idx < count; idx++ {
		s = append(s, fmt.Sprintf("%v-%v", prefix, idx))
	}

	return s
}

func generateLabels(names []string, values []string) []labels.Labels {
	l := make([]labels.Labels, 0, len(names)*len(values))

	for _, name := range names {
		for _, value := range values {
			l = append(l, labels.FromStrings(name, value))
		}
	}

	return l
}

func reverseLookup(b *testing.B, reader *StreamBinaryReader, symbols []string) ([]uint32, map[uint32]string) {
	i := make([]uint32, 0, len(symbols))
	m := make(map[uint32]string, len(symbols))

	for _, s := range symbols {
		idx, err := reader.symbols.ReverseLookup(s)
		require.NoError(b, err)
		m[idx] = s
		i = append(i, idx)
	}

	return i, m
}
