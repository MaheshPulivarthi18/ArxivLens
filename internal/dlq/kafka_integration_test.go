//go:build integration

package dlq

// SKELETON: TestReadStopsAtSnapshot_Integration — create a 3-partition topic (one empty), 1,500 records in
// chunks of 25, FetchMaxBytes 4 KiB (>= 5 polls), records past the snapshot are not returned, stall on until > High. [~120]
