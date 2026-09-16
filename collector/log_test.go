package collector

import (
	"testing"

	"github.com/pingcap/diag/scraper"
	"github.com/stretchr/testify/require"
)

func TestLogTypesToScrap(t *testing.T) {
	assert := require.New(t)

	cases := []struct {
		name      string
		collector collectLog
		expected  []string
	}{
		{
			name:      "std only",
			collector: collectLog{Std: true},
			expected:  []string{scraper.LogTypeStd},
		},
		{
			name:      "slow only",
			collector: collectLog{Slow: true},
			expected:  []string{scraper.LogTypeSlow},
		},
		{
			name:      "unknown only",
			collector: collectLog{Unknown: true},
			expected:  []string{scraper.LogTypeUnknown},
		},
		{
			name:      "std, slow and unknown",
			collector: collectLog{Std: true, Slow: true, Unknown: true},
			expected:  []string{scraper.LogTypeStd, scraper.LogTypeSlow, scraper.LogTypeUnknown},
		},
		{
			// rocksdb logs are scrapped from the TiKV data directory by a
			// dedicated step, so nothing is left for the generic scraper step
			// and it must not be built with an empty --logtype value
			name:      "rocksdb only",
			collector: collectLog{Rocksdb: true},
			expected:  nil,
		},
		{
			name:      "nothing selected",
			collector: collectLog{},
			expected:  nil,
		},
	}

	for _, cs := range cases {
		opt := &LogCollectOptions{collector: cs.collector}
		assert.Equal(cs.expected, opt.logTypesToScrap(), cs.name)
	}
}

func TestMergeFileStats(t *testing.T) {
	assert := require.New(t)

	// several TiKV instances on the same host are scrapped one by one, every
	// one of them has to be kept in the result
	fileStats := map[string][]CollectStat{}
	mergeFileStats(fileStats, map[string][]CollectStat{
		"127.0.0.1": {{Target: "/data/db/data/tikv-20160/rocksdb.info", Size: 1}},
	})
	mergeFileStats(fileStats, map[string][]CollectStat{
		"127.0.0.1": {{Target: "/data/db/data/tikv-20161/rocksdb.info", Size: 2}},
	})

	assert.Equal([]CollectStat{
		{Target: "/data/db/data/tikv-20160/rocksdb.info", Size: 1},
		{Target: "/data/db/data/tikv-20161/rocksdb.info", Size: 2},
	}, fileStats["127.0.0.1"])

	// hosts of other nodes are merged independently
	mergeFileStats(fileStats, map[string][]CollectStat{
		"127.0.0.2": {{Target: "/data/db/data/tikv-20160/rocksdb.info", Size: 3}},
	})
	assert.Len(fileStats["127.0.0.2"], 1)
	assert.Len(fileStats["127.0.0.1"], 2)
}
