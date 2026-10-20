package artifacts

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestArtifact(t *testing.T) (*Store, string) {
	t.Helper()
	st := newTestStore(t)
	res, err := st.Publish(t.Context(), PublishInput{Content: strPtr(fullPage)})
	require.NoError(t, err)
	return st, res.Meta.ID
}

func TestDocsWriteQueryAndBatch(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	ctx := t.Context()

	docs, err := st.WriteDocs(ctx, id, []DocWrite{
		{Op: "set", Collection: "books", DocID: "dune", Data: map[string]any{"title": "Dune", "year": 1965.0}},
		{Op: "set", Collection: "books", DocID: "anathem", Data: map[string]any{"title": "Anathem", "year": 2008.0}},
		{Op: "set", Collection: "books", Data: map[string]any{"title": "Untitled"}},
	})
	require.NoError(t, err)
	require.Len(t, docs, 3)
	require.NotEmpty(t, docs[2].ID)

	_, err = st.WriteDocs(ctx, id, []DocWrite{{Op: "update", Collection: "books", DocID: "dune", Data: map[string]any{"read": true}}})
	require.NoError(t, err)
	d, err := st.GetDoc(id, "books", "dune")
	require.NoError(t, err)
	require.Equal(t, "Dune", d.Data["title"])
	require.Equal(t, true, d.Data["read"])

	got, err := st.ListDocs(id, "books", QueryOptions{OrderBy: "year", Desc: true, Limit: 1})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "anathem", got[0].ID)

	got, err = st.ListDocs(id, "books", QueryOptions{Where: map[string]any{"read": true}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "dune", got[0].ID)

	cols, err := st.Collections(id)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"books": 3}, cols)

	// A batch with one bad write changes nothing.
	_, err = st.WriteDocs(ctx, id, []DocWrite{
		{Op: "delete", Collection: "books", DocID: "dune"},
		{Op: "explode", Collection: "books", DocID: "x"},
	})
	require.Error(t, err)
	_, err = st.GetDoc(id, "books", "dune")
	require.NoError(t, err)

	_, err = st.WriteDocs(ctx, id, []DocWrite{{Op: "set", Collection: "bad name", DocID: "x", Data: map[string]any{}}})
	require.Error(t, err)
}

func TestCommentsThreadsAndResolve(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	ctx := t.Context()

	root, err := st.AddComment(ctx, id, Comment{Body: "Make the header blue", ToAgent: true})
	require.NoError(t, err)
	require.Equal(t, AuthorYou, root.Author)
	_, err = st.AddComment(ctx, id, Comment{Parent: root.ID, Body: "Done", Author: AuthorAgent})
	require.NoError(t, err)
	_, err = st.AddComment(ctx, id, Comment{Body: "   "})
	require.Error(t, err)

	list, err := st.Comments(id)
	require.NoError(t, err)
	require.Len(t, list, 2)

	require.NoError(t, st.ResolveComment(ctx, id, list[1].ID, true))
	list, err = st.Comments(id)
	require.NoError(t, err)
	for _, c := range list {
		require.True(t, c.Resolved)
	}
	require.Error(t, st.ResolveComment(ctx, id, "missing", true))
}

func TestStorage(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	ctx := t.Context()

	require.NoError(t, st.SetStorage(ctx, id, "tab", strPtr("2"), false))
	m, err := st.Storage(id)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"tab": "2"}, m)

	require.NoError(t, st.SetStorage(ctx, id, "tab", nil, false))
	m, err = st.Storage(id)
	require.NoError(t, err)
	require.Empty(t, m)
}
