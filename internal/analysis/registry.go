package analysis

import (
	"errors"

	"github.com/ben-ranford/lopper/internal/lang/cpp"
	"github.com/ben-ranford/lopper/internal/lang/dart"
	"github.com/ben-ranford/lopper/internal/lang/dotnet"
	"github.com/ben-ranford/lopper/internal/lang/elixir"
	"github.com/ben-ranford/lopper/internal/lang/golang"
	"github.com/ben-ranford/lopper/internal/lang/js"
	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/kotlinandroid"
	"github.com/ben-ranford/lopper/internal/lang/php"
	"github.com/ben-ranford/lopper/internal/lang/powershell"
	"github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/lang/ruby"
	"github.com/ben-ranford/lopper/internal/lang/rust"
	"github.com/ben-ranford/lopper/internal/lang/swift"
	"github.com/ben-ranford/lopper/internal/language"
)

type adapterFactory func() language.Adapter

var (
	errNilAdapterFactory    = errors.New("adapter factory is nil")
	defaultAdapterFactories = []adapterFactory{
		func() language.Adapter { return js.NewAdapter() },
		func() language.Adapter { return python.NewAdapter() },
		func() language.Adapter { return cpp.NewAdapter() },
		func() language.Adapter { return jvm.NewAdapter() },
		func() language.Adapter { return kotlinandroid.NewAdapter() },
		func() language.Adapter { return golang.NewAdapter() },
		func() language.Adapter { return php.NewAdapter() },
		func() language.Adapter { return rust.NewAdapter() },
		func() language.Adapter { return ruby.NewAdapter() },
		func() language.Adapter { return dotnet.NewAdapter() },
		func() language.Adapter { return elixir.NewAdapter() },
		func() language.Adapter { return swift.NewAdapter() },
		func() language.Adapter { return dart.NewAdapter() },
		func() language.Adapter { return powershell.NewAdapter() },
	}
)

func NewService() *Service {
	registry, err := newDefaultRegistry(defaultAdapterFactories)
	return &Service{
		Registry: registry,
		InitErr:  err,
	}
}

func newDefaultRegistry(factories []adapterFactory) (*language.Registry, error) {
	registry := language.NewRegistry()
	return registry, registerAdapters(registry, factories)
}

func registerAdapters(registry *language.Registry, factories []adapterFactory) error {
	if registry == nil {
		return errors.New("language registry is not configured")
	}
	for _, factory := range factories {
		if factory == nil {
			return errNilAdapterFactory
		}
		if err := registry.Register(factory()); err != nil {
			return err
		}
	}
	return nil
}
