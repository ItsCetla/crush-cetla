package clipboard

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLiveImageRead puts a PNG on the real system clipboard and reads it
// back as an image. It replaces the clipboard, so it only runs when
// TADA_LIVE_CLIPBOARD=1 is set.
func TestLiveImageRead(t *testing.T) {
	if os.Getenv("TADA_LIVE_CLIPBOARD") != "1" {
		t.Skip("set TADA_LIVE_CLIPBOARD=1 to use the real clipboard")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("only scripted on macOS")
	}
	require.NoError(t, Init())

	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := range 30 {
		for x := range 40 {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	path := filepath.Join(t.TempDir(), "shot.png")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	script := `set the clipboard to (read (POSIX file "` + path + `") as «class PNGf»)`
	out, err := exec.CommandContext(t.Context(), "osascript", "-e", script).CombinedOutput()
	require.NoError(t, err, string(out))

	data, err := Read(FormatImage)
	require.NoError(t, err)
	got, format, err := image.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, "png", format)
	require.Equal(t, img.Bounds(), got.Bounds())
	r, g, b, _ := got.At(10, 10).RGBA()
	require.Equal(t, [3]uint32{60, 80, 200}, [3]uint32{r >> 8, g >> 8, b >> 8})
}
