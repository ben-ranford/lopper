package cpp

import "time"

type Adapter struct {
	Clock func() time.Time
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "cpp"
}

func (a *Adapter) Aliases() []string {
	return []string{"c++", "c", "cc", "cxx"}
}
