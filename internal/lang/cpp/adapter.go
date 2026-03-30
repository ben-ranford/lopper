package cpp

import "github.com/ben-ranford/lopper/internal/language"

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("cpp", []string{"c++", "c", "cc", "cxx"}, adapter.DetectWithConfidence)
	return adapter
}
