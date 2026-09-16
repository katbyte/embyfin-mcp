// Package pointer converts between values and pointers in expression position.
//
// API models are full of optional fields typed as *T. Reading one means
// "the value, or the zero value when it is absent", and building one means
// "a pointer to this literal". Both are trivial as statements and awkward as
// expressions, which is where these helpers earn their keep: inside a struct
// literal, a function argument, or a return.
package pointer

// From dereferences p, returning the zero value of T when p is nil. Use it to
// read an optional API field without a nil check at every site.
func From[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// To returns a pointer to a copy of v. Go 1.26's new(expr) does the same, and
// linters will suggest it; To remains for call sites that read better as a
// named conversion, such as method chains and conversions of typed constants.
func To[T any](v T) *T {
	return new(v)
}
