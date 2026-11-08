package arxiv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionSuffix matches the trailing version of an arXiv ID ("v2" in "2401.12345v2").
var versionSuffix = regexp.MustCompile(`v(\d+)$`)

// SplitVersion splits an Atom entry ID ("http://arxiv.org/abs/2401.12345v2") or a bare ID
// ("2401.12345v2") into the ID without version and the version. Anything up to "/abs/" is
// dropped. ok is false when there is no version suffix or it is not a positive integer; id is
// still returned (without any suffix), and the caller decides what a missing version means.
func SplitVersion(entryID string) (id string, version int, ok bool) {
	id = strings.TrimSpace(entryID)
	if i := strings.LastIndex(id, "/abs/"); i >= 0 {
		id = id[i+len("/abs/"):]
	}
	m := versionSuffix.FindStringSubmatch(id)
	if m == nil {
		return id, 0, false
	}
	id = strings.TrimSuffix(id, m[0])
	v, err := strconv.Atoi(m[1])
	if err != nil || v < 1 {
		return id, 0, false
	}
	return id, v, true
}

// PDFURL is the arXiv URL of one specific version's PDF. The unversioned URL serves whatever
// version is latest at fetch time, so it must not be used when the version is recorded.
func PDFURL(id string, version int) string {
	return fmt.Sprintf("https://arxiv.org/pdf/%sv%d.pdf", id, version)
}
