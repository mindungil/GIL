package sample

type Greeter interface {
	Greet(name string) string
}

type Hello struct {
	Prefix string
}

func (h *Hello) Greet(name string) string {
	return h.Prefix + " " + name
}

func NewHello(prefix string) *Hello {
	return &Hello{Prefix: prefix}
}

const Default = "Hi"
var Counter = 0
