package artifacts

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Doc is one document in an artifact's database.
type Doc struct {
	ID      string         `json:"id"`
	Data    map[string]any `json:"data"`
	Created time.Time      `json:"created"`
	Updated time.Time      `json:"updated"`
}

// Comment is one comment on an artifact. A thread root has no parent.
type Comment struct {
	ID       string    `json:"id"`
	Parent   string    `json:"parent"`
	Author   string    `json:"author"`
	Body     string    `json:"body"`
	Anchor   string    `json:"anchor,omitempty"`
	Version  int       `json:"version,omitempty"`
	ToAgent  bool      `json:"to_agent"`
	Resolved bool      `json:"resolved"`
	Created  time.Time `json:"created"`
}

// Comment authors.
const (
	AuthorYou   = "you"
	AuthorAgent = "agent"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type database map[string]map[string]*Doc

func (s *Store) dataPath(id string) string     { return filepath.Join(s.dir(id), "data.json") }
func (s *Store) commentsPath(id string) string { return filepath.Join(s.dir(id), "comments.json") }
func (s *Store) storagePath(id string) string  { return filepath.Join(s.dir(id), "storage.json") }

func (s *Store) loadDB(id string) (database, error) {
	db := database{}
	if err := readJSON(s.dataPath(id), &db); err != nil && !isNotExist(err) {
		return nil, err
	}
	return db, nil
}

func checkNames(collection, docID string) error {
	if !nameRe.MatchString(collection) {
		return fmt.Errorf("collection %q: use 1-128 letters, digits, _ . : -", collection)
	}
	if docID != "" && !nameRe.MatchString(docID) {
		return fmt.Errorf("doc id %q: use 1-128 letters, digits, _ . : -", docID)
	}
	return nil
}

// GetDoc returns one document, or nil when it doesn't exist.
func (s *Store) GetDoc(id, collection, docID string) (*Doc, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if err := checkNames(collection, docID); err != nil {
		return nil, err
	}
	db, err := s.loadDB(id)
	if err != nil {
		return nil, err
	}
	return db[collection][docID], nil
}

// QueryOptions filters ListDocs.
type QueryOptions struct {
	// Where keeps docs whose top-level fields equal these values.
	Where map[string]any
	// OrderBy sorts by a top-level field (default: created time).
	OrderBy string
	Desc    bool
	Limit   int
}

// ListDocs returns a collection's documents, oldest first by default.
func (s *Store) ListDocs(id, collection string, q QueryOptions) ([]*Doc, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if err := checkNames(collection, ""); err != nil {
		return nil, err
	}
	db, err := s.loadDB(id)
	if err != nil {
		return nil, err
	}
	out := []*Doc{}
	for _, d := range db[collection] {
		if matches(d, q.Where) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b *Doc) int {
		c := 0
		if q.OrderBy != "" {
			// Documents without the field go last in either direction.
			va, okA := a.Data[q.OrderBy]
			vb, okB := b.Data[q.OrderBy]
			okA, okB = okA && va != nil, okB && vb != nil
			switch {
			case okA && !okB:
				return -1
			case !okA && okB:
				return 1
			case okA && okB:
				c = compareValues(va, vb)
			}
		}
		if c == 0 {
			c = a.Created.Compare(b.Created)
		}
		if c == 0 {
			c = strings.Compare(a.ID, b.ID)
		}
		if q.Desc {
			return -c
		}
		return c
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// DocWrite is one write in a batch.
type DocWrite struct {
	Op         string         `json:"op"`
	Collection string         `json:"collection"`
	DocID      string         `json:"doc_id,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// WriteDocs applies writes atomically: either all of them land or none.
// It returns the resulting document of each write (nil for deletes).
func (s *Store) WriteDocs(ctx context.Context, id string, writes []DocWrite) ([]*Doc, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if len(writes) == 0 {
		return nil, errors.New("no writes")
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	db, err := s.loadDB(id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]*Doc, len(writes))
	for i, w := range writes {
		if err := checkNames(w.Collection, w.DocID); err != nil {
			return nil, err
		}
		coll := db[w.Collection]
		if coll == nil {
			coll = map[string]*Doc{}
			db[w.Collection] = coll
		}
		switch w.Op {
		case "set", "update":
			docID := w.DocID
			if docID == "" {
				docID = newID()
			}
			old := coll[docID]
			doc := &Doc{ID: docID, Created: now, Updated: now, Data: map[string]any{}}
			if old != nil {
				doc.Created = old.Created
				if w.Op == "update" {
					doc.Data = maps.Clone(old.Data)
				}
			} else if w.Op == "update" && w.DocID != "" {
				return nil, fmt.Errorf("update: %s/%s doesn't exist; use set", w.Collection, w.DocID)
			}
			maps.Copy(doc.Data, w.Data)
			// An update sets a field to null to drop it.
			for k, v := range doc.Data {
				if v == nil {
					delete(doc.Data, k)
				}
			}
			b, _ := json.Marshal(doc.Data)
			if len(b) > maxDocBytes {
				return nil, fmt.Errorf("%s/%s is %d bytes; a document may hold 1MB", w.Collection, docID, len(b))
			}
			coll[docID] = doc
			out[i] = doc
		case "delete":
			if w.DocID == "" {
				return nil, errors.New("delete needs doc_id")
			}
			delete(coll, w.DocID)
		default:
			return nil, fmt.Errorf("unknown write op %q: use set, update or delete", w.Op)
		}
		if len(coll) == 0 {
			delete(db, w.Collection)
		}
	}
	if err := writeJSON(s.dataPath(id), db); err != nil {
		return nil, err
	}
	return out, nil
}

// Collections lists an artifact's collection names with their sizes.
func (s *Store) Collections(id string) (map[string]int, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	db, err := s.loadDB(id)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for k, v := range db {
		out[k] = len(v)
	}
	return out, nil
}

func matches(d *Doc, where map[string]any) bool {
	for k, want := range where {
		got, ok := d.Data[k]
		if k == "id" && !ok {
			got, ok = d.ID, true
		}
		if !ok || !equalJSON(got, want) {
			return false
		}
	}
	return true
}

func equalJSON(a, b any) bool {
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return fa == fb
		}
	}
	return reflect.DeepEqual(a, b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func compareValues(a, b any) int {
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return cmp.Compare(fa, fb)
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// Comments returns an artifact's comments, oldest first.
func (s *Store) Comments(id string) ([]Comment, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	out := []Comment{}
	if err := readJSON(s.commentsPath(id), &out); err != nil && !isNotExist(err) {
		return nil, err
	}
	return out, nil
}

// AddComment adds a comment or a reply (c.Parent set).
func (s *Store) AddComment(ctx context.Context, id string, c Comment) (*Comment, error) {
	c.Body = strings.TrimSpace(c.Body)
	if c.Body == "" {
		return nil, errors.New("comment body is empty")
	}
	if len(c.Body) > 20000 {
		return nil, errors.New("comment is longer than 20000 characters")
	}
	m, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	list, err := s.Comments(id)
	if err != nil {
		return nil, err
	}
	if c.Parent != "" {
		i := slices.IndexFunc(list, func(x Comment) bool { return x.ID == c.Parent })
		if i < 0 {
			return nil, fmt.Errorf("comment %s not found", c.Parent)
		}
		// Replies always hang off the thread root.
		if list[i].Parent != "" {
			c.Parent = list[i].Parent
		}
	}
	c.ID = "c" + newID()
	c.Author = cmp.Or(c.Author, AuthorYou)
	c.Created = time.Now().UTC()
	if c.Version == 0 {
		c.Version = m.Latest()
	}
	list = append(list, c)
	if err := writeJSON(s.commentsPath(id), list); err != nil {
		return nil, err
	}
	return &c, nil
}

// ResolveComment marks a thread (resolved) or reopens it.
func (s *Store) ResolveComment(ctx context.Context, id, commentID string, resolved bool) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	list, err := s.Comments(id)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(x Comment) bool { return x.ID == commentID })
	if i < 0 {
		return fmt.Errorf("comment %s not found", commentID)
	}
	root := commentID
	if list[i].Parent != "" {
		root = list[i].Parent
	}
	for j := range list {
		if list[j].ID == root || list[j].Parent == root {
			list[j].Resolved = resolved
		}
	}
	return writeJSON(s.commentsPath(id), list)
}

// Storage returns an artifact's per-viewer storage (a key/value map that
// stands in for localStorage inside the sandboxed page).
func (s *Store) Storage(id string) (map[string]string, error) {
	out := map[string]string{}
	if err := readJSON(s.storagePath(id), &out); err != nil && !isNotExist(err) {
		return nil, err
	}
	return out, nil
}

// SetStorage sets (value non-nil), removes (nil) or clears (clear) keys.
func (s *Store) SetStorage(ctx context.Context, id, key string, value *string, clearAll bool) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := s.Storage(id)
	if err != nil {
		return err
	}
	switch {
	case clearAll:
		clear(st)
	case key == "":
		return errors.New("storage key is empty")
	case value == nil:
		delete(st, key)
	default:
		st[key] = *value
	}
	size := 0
	for k, v := range st {
		size += len(k) + len(v)
	}
	if size > maxStorageBytes {
		return errors.New("storage is full (5MB)")
	}
	return writeJSON(s.storagePath(id), st)
}
