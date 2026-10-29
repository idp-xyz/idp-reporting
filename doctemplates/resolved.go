package doctemplates

// Resolved is what a renderer needs of a schema 2.1 document besides the values bound from the snapshot (ADR 0005): the bytes of
// the image each image element draws, by element id, which the caller resolved with the owner's authorization and which the
// renderer checks against the digest the template names; and, when a document is issued, the number the source product assigned,
// printed wherever the document shows its issue number. Previews and the pre-issue check carry no number: a preview prints a
// placeholder and the check proves the layout for every number the product may assign.
type Resolved struct {
	Images map[string][]byte
	Number string
}
