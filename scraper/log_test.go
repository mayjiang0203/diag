// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package scraper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pingcap/diag/collector/log/parser"
	"github.com/stretchr/testify/require"
)

// A rocksdb log as TiKV writes it. Every line is stamped, including the RocksDB
// version and options block: RocksDB emits that block through
// Logger::LogHeader, whose default implementation logs it at INFO level, so it
// is neither tagged as a header nor written without a timestamp. Only the
// continuation lines of a record that spans several physical lines have no
// timestamp of their own.
const rocksdbFixture = `[2024/01/01 09:59:59.000 +08:00][5][INFO] RocksDB version: 8.11.3
[2024/01/01 09:59:59.000 +08:00][5][INFO] Options.error_if_exists: 0
[2024/01/01 10:00:00.000 +08:00][12345][INFO] first message
[2024/01/01 10:30:00.000 +08:00][12345][INFO] second message
[2024/01/01 11:00:00.000 +08:00][12345][WARN] third message
  continuation of the third message
[2024/01/01 12:00:00.000 +08:00][12345][INFO] fourth message
`

// rocksdbUnstamped stands for a log whose lines carry no timestamp at all, as
// produced when log.enable-timestamp is disabled.
const rocksdbUnstamped = `RocksDB version: 8.11.3
Options.error_if_exists: 0
12345 [db/version_set.cc:1] no timestamp here
`

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(parser.TimeStampLayout, s)
	require.NoError(t, err)
	return v
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	fp := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(fp, []byte(content), 0o644))
	return fp
}

func TestRocksDBLineTime(t *testing.T) {
	assert := require.New(t)

	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"[2024/01/01 12:00:00.000 +08:00][12345][INFO] hello", "2024/01/01 12:00:00.000 +08:00", true},
		{"[2024/01/01 12:00:00.000 -05:00][1][WARN] hello", "2024/01/01 12:00:00.000 -05:00", true},
		// the RocksDB version line is stamped like any other line
		{"[2024/01/01 12:00:00.000 +08:00][5][INFO] RocksDB version: 8.10.2", "2024/01/01 12:00:00.000 +08:00", true},
		// a continuation line of a multi-line record
		{"  continuation of the previous message", "", false},
		// only the leading timestamp matters, the shape of the rest of the line
		// is not checked: the extractor is applied to files that the file name
		// already classified as rocksdb logs
		{"[2024/01/01 12:00:00.000 +08:00] [INFO] [mod.rs:1] [\"msg\"]", "2024/01/01 12:00:00.000 +08:00", true},
	}

	for _, cs := range cases {
		got, ok := rocksDBLineTime([]byte(cs.line))
		assert.Equal(cs.ok, ok, cs.line)
		if cs.ok {
			assert.Equal(cs.want, got.Format(parser.TimeStampLayout), cs.line)
		}
	}
}

func TestStdAndSlowLineTime(t *testing.T) {
	assert := require.New(t)

	// a component log line
	got, ok := stdLineTime([]byte(`[2024/01/01 12:00:00.000 +08:00] [INFO] [mod.rs:1] ["msg"]`))
	assert.True(ok)
	assert.Equal("2024/01/01 12:00:00.000 +08:00", got.Format(parser.TimeStampLayout))

	// a continuation line has no timestamp of its own
	_, ok = stdLineTime([]byte("  at main.go:12"))
	assert.False(ok)

	// a slow query log header
	got, ok = slowLineTime([]byte("# Time: 2024-01-01T12:00:00.000000+08:00"))
	assert.True(ok)
	assert.Equal(int64(1704081600), got.Unix())

	assert.Nil(lineTimeOf(LogTypeUnknown), "unknown logs cannot be trimmed line by line")
}

func TestFileHeadInRange(t *testing.T) {
	assert := require.New(t)
	dir := t.TempDir()
	fp := writeFile(t, dir, "rocksdb.info", rocksdbFixture)
	fi, err := os.Stat(fp)
	assert.NoError(err)

	// the file overlaps the range
	assert.True(fileHeadInRange(fp, fi, rocksDBLineTime,
		mustTime(t, "2024/01/01 09:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 10:30:00.000 +08:00")))

	// the file starts after the end of the range, so it is newer than what is
	// asked for
	assert.False(fileHeadInRange(fp, fi, rocksDBLineTime,
		mustTime(t, "2024/01/01 08:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 09:00:00.000 +08:00")))

	// nothing was appended after the range began
	old := mustTime(t, "2023/12/31 23:00:00.000 +08:00")
	assert.NoError(os.Chtimes(fp, old, old))
	fi, err = os.Stat(fp)
	assert.NoError(err)
	assert.False(fileHeadInRange(fp, fi, rocksDBLineTime,
		mustTime(t, "2024/01/01 09:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 10:00:00.000 +08:00")))

	// a file without any timestamp is kept instead of being dropped
	unstamped := writeFile(t, dir, "rocksdb-unstamped.info", rocksdbUnstamped)
	fi, err = os.Stat(unstamped)
	assert.NoError(err)
	assert.True(fileHeadInRange(unstamped, fi, rocksDBLineTime,
		mustTime(t, "2024/01/01 09:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 10:00:00.000 +08:00")))
}

func TestTrimToRange(t *testing.T) {
	assert := require.New(t)
	dir := t.TempDir()
	src := writeFile(t, dir, "rocksdb.info", rocksdbFixture)
	dst := filepath.Join(dir, "out", "rocksdb.info")

	// out of range lines are dropped, the continuation line of a kept record
	// survives with it
	written, found, err := trimToRange(src, dst, rocksDBLineTime,
		mustTime(t, "2024/01/01 10:15:00.000 +08:00"),
		mustTime(t, "2024/01/01 11:30:00.000 +08:00"))
	assert.NoError(err)
	assert.True(found)
	expected := `[2024/01/01 10:30:00.000 +08:00][12345][INFO] second message
[2024/01/01 11:00:00.000 +08:00][12345][WARN] third message
  continuation of the third message
`
	assert.Equal(int64(len(expected)), written)
	content, err := os.ReadFile(dst)
	assert.NoError(err)
	assert.Equal(expected, string(content))

	// a dropped record takes its continuation lines with it
	_, _, err = trimToRange(src, dst, rocksDBLineTime,
		mustTime(t, "2024/01/01 10:30:00.000 +08:00"),
		mustTime(t, "2024/01/01 10:45:00.000 +08:00"))
	assert.NoError(err)
	content, err = os.ReadFile(dst)
	assert.NoError(err)
	assert.Equal("[2024/01/01 10:30:00.000 +08:00][12345][INFO] second message\n", string(content))

	// a line without any timestamp is reported as "not trimmable"
	_, found, err = trimToRange(writeFile(t, dir, "unstamped.info", rocksdbUnstamped), dst,
		rocksDBLineTime,
		mustTime(t, "2024/01/01 10:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 11:00:00.000 +08:00"))
	assert.NoError(err)
	assert.False(found)

	// an empty file is not trimmable either
	_, found, err = trimToRange(writeFile(t, dir, "empty.info", ""), dst,
		rocksDBLineTime,
		mustTime(t, "2024/01/01 10:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 11:00:00.000 +08:00"))
	assert.NoError(err)
	assert.False(found)
}

func TestTrimToRangeKeepsLeadingUnstampedLine(t *testing.T) {
	assert := require.New(t)
	dir := t.TempDir()

	// A file may start with an unstamped line, e.g. a record split by a
	// rotation. It has nothing to inherit from, so it is kept.
	src := writeFile(t, dir, "split.info",
		"  tail of a record split by a rotation\n"+
			"[2024/01/01 12:00:00.000 +08:00][1][INFO] out of range\n")
	dst := filepath.Join(dir, "out", "split.info")

	_, found, err := trimToRange(src, dst, rocksDBLineTime,
		mustTime(t, "2024/01/01 10:00:00.000 +08:00"),
		mustTime(t, "2024/01/01 11:00:00.000 +08:00"))
	assert.NoError(err)
	assert.True(found)

	content, err := os.ReadFile(dst)
	assert.NoError(err)
	assert.Equal("  tail of a record split by a rotation\n", string(content))
}

func TestTrimmedPath(t *testing.T) {
	assert := require.New(t)
	trim := t.TempDir()

	// the whole source path is kept, so two instances of the same host do not
	// collide on their rocksdb.info
	a := trimmedPath(trim, "/data1/tidb-data/tikv-20160/rocksdb.info")
	b := trimmedPath(trim, "/data1/tidb-data/tikv-20161/rocksdb.info")
	assert.NotEqual(a, b)
	assert.Equal(filepath.Join(trim, "data1/tidb-data/tikv-20160/rocksdb.info"), a)
	assert.Equal(filepath.Join(trim, "data1/tidb-data/tikv-20161/rocksdb.info"), b)

	// a relative source path stays inside the trim directory
	rel := trimmedPath(trim, "../../etc/passwd")
	assert.True(strings.HasPrefix(rel, trim+string(filepath.Separator)), rel)
	assert.NotContains(rel, "..")
}

func TestLogScraperTrim(t *testing.T) {
	assert := require.New(t)
	dataDir := t.TempDir()
	trimDir := t.TempDir()

	active := writeFile(t, dataDir, "rocksdb.info", rocksdbFixture)
	// a rotated log from before the range: it must not be downloaded at all
	rotated := writeFile(t, dataDir, "rocksdb-2023-12-30T00-00-00.000.info",
		"[2023/12/30 00:00:00.000 +08:00][1][INFO] ancient\n")
	old := mustTime(t, "2023/12/30 01:00:00.000 +08:00")
	assert.NoError(os.Chtimes(rotated, old, old))
	// an unrelated file of the data directory must not be collected
	writeFile(t, dataDir, "000123.sst", "\x00\x01\x02")

	start := mustTime(t, "2024/01/01 10:15:00.000 +08:00")
	end := mustTime(t, "2024/01/01 11:30:00.000 +08:00")

	s := &LogScraper{
		Paths:   []string{filepath.Join(dataDir, "*")},
		Types:   map[string]bool{LogTypeRocksDB: true},
		Start:   start,
		End:     end,
		Trim:    true,
		TrimDir: trimDir,
	}
	sample := &Sample{}
	assert.NoError(s.Scrap(sample))

	assert.Len(sample.Log, 1, "only the active rocksdb log is in range")
	target := trimmedPath(trimDir, active)
	expected := `[2024/01/01 10:30:00.000 +08:00][12345][INFO] second message
[2024/01/01 11:00:00.000 +08:00][12345][WARN] third message
  continuation of the third message
`
	assert.Equal(int64(len(expected)), sample.Log[target])
	assert.Equal(LogTypeRocksDB, sample.LogTypes[target])

	content, err := os.ReadFile(target)
	assert.NoError(err)
	assert.Equal(expected, string(content))
	assert.NotContains(string(content), "first message")
	assert.NotContains(string(content), "fourth message")

	// without trimming the whole file is reported
	s.Trim = false
	sample = &Sample{}
	assert.NoError(s.Scrap(sample))
	assert.Len(sample.Log, 1)
	assert.Equal(int64(len(rocksdbFixture)), sample.Log[active])

	// trimming requires a directory to write to
	s.Trim = true
	s.TrimDir = ""
	assert.Error(s.Scrap(&Sample{}))
}

func TestLogScraperTrimFallsBackToWholeFile(t *testing.T) {
	assert := require.New(t)
	dataDir := t.TempDir()
	trimDir := t.TempDir()

	// log.enable-timestamp = false makes TiKV write rocksdb lines without a
	// timestamp. Nothing can be trimmed, the file has to be collected as is.
	fp := writeFile(t, dataDir, "rocksdb.info", rocksdbUnstamped)

	s := &LogScraper{
		Paths:   []string{filepath.Join(dataDir, "*")},
		Types:   map[string]bool{LogTypeRocksDB: true},
		Start:   mustTime(t, "2024/01/01 10:00:00.000 +08:00"),
		End:     mustTime(t, "2024/01/01 11:00:00.000 +08:00"),
		Trim:    true,
		TrimDir: trimDir,
	}
	sample := &Sample{}
	assert.NoError(s.Scrap(sample))

	assert.Len(sample.Log, 1)
	assert.Equal(int64(len(rocksdbUnstamped)), sample.Log[fp],
		"the original file is reported when nothing can be trimmed")
	_, err := os.Stat(trimmedPath(trimDir, fp))
	assert.True(os.IsNotExist(err), "no trimmed copy must be left behind")
}
