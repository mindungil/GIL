package pkg

// Util is a utility helper.
type Util struct {
	Name string
}

// Do performs a utility action.
func Do(u Util) string {
	return u.Name
}
