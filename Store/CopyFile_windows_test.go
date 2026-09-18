//go:build windows

package Store

import (
	"path/filepath"
	"testing"

	vfs "github.com/spf13/afero"

	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func TestCopyFileOS_RealPathRequiredOnWindows(t *testing.T) {
	old := vfsx.UseCopyFileOS
	vfsx.UseCopyFileOS = true
	defer func() { vfsx.UseCopyFileOS = old }()

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	s := NewStore(vfs.NewMemMapFs())

	if err := c.Rescue(func() { s.Store(InRoot, src, "u", false, nil, true) }); err == nil {
		t.Fatal("Windows production copy must require RealPath instead of falling back")
	}
}
