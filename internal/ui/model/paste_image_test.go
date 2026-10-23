package model

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/stretchr/testify/require"
)

func TestImageAttachment(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))))

	att, ok := imageAttachment(buf.Bytes(), 3).(message.Attachment)
	require.True(t, ok)
	require.Equal(t, "paste_3.png", att.FileName)
	require.Equal(t, "image/png", att.MimeType)
	require.Equal(t, buf.Bytes(), att.Content)

	big := make([]byte, common.MaxAttachmentSize+1)
	info, ok := imageAttachment(big, 1).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
}

func TestPasteNameCountsImages(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"paste_1.txt":   true,
		"paste_12.png":  true,
		"paste_1xtxt":   false,
		"mypaste_1.png": false,
		"paste_1.jpg":   false,
	} {
		require.Equal(t, want, pasteRE.MatchString(name), name)
	}
}

func TestPasteImageKeys(t *testing.T) {
	t.Parallel()

	km := DefaultKeyMap()
	require.Contains(t, km.Editor.PasteImage.Keys(), "ctrl+v")
	require.Contains(t, km.Editor.PasteImage.Keys(), "alt+i",
		"alt+i must paste images where the terminal keeps ctrl+v")
}

func TestEmptyPasteWithoutImageModel(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	// The stub config has no coder model, so it can't take images and an
	// empty paste does nothing instead of reading the clipboard.
	require.Nil(t, u.handlePasteMsg(tea.PasteMsg{Content: "  \r\n"}))
}
