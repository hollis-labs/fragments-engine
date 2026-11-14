package domain

import "time"

// ReaderCollectionDefaultKind is the kind a collection has when none is given.
// Kind is a display hint only; playlist, reading_list and research are the
// well-known values.
const ReaderCollectionDefaultKind = "collection"

// ReaderCollection is a principal-owned, ordered set of fragments.
type ReaderCollection struct {
	ID          string    `json:"id"`
	PrincipalID string    `json:"principal_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	ItemCount   int       `json:"item_count"`
	Revision    int       `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ReaderCollectionRef names one collection a fragment is in.
type ReaderCollectionRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
