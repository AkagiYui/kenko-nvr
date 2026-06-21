//go:build !unix

package recording

// diskUsage is unavailable on this platform; the free-space retention rule is
// effectively disabled (reports a large free value).
func diskUsage(path string) (free, total uint64, err error) {
	return 1 << 62, 1 << 62, nil
}
