package jvm

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type jvmGradleDiscovery struct {
	budget   *shared.GradleDiscoveryBudget
	resolver *shared.GradleCatalogResolver
}

func (c *buildFileWarningCollector) visitGradleWithinRoot(ctx context.Context, root safeio.Root, leaf, path string) error {
	data, err := c.gradle.budget.ReadWithinRoot(ctx, root, leaf, path)
	if err != nil {
		return err
	}
	coordinates, err := shared.ParseGradleDependencyCoordinatesForFileContext(ctx, path, data)
	if err != nil {
		return err
	}
	catalogs, warnings, err := c.gradle.resolver.ParseDependencyReferencesContext(ctx, path, data)
	if err != nil {
		return err
	}
	c.warnings = append(c.warnings, warnings...)
	for _, item := range coordinates {
		c.retainGradleCoordinate(item.Group, item.Artifact)
	}
	for _, item := range catalogs {
		c.retainGradleCoordinate(item.Group, item.Artifact)
	}
	return shared.GradleDiscoveryFailure(path, "parse", ctx.Err())
}

func (c *buildFileWarningCollector) retainGradleCoordinate(group, artifact string) {
	key := group + ":" + artifact
	if _, exists := c.seen[key]; exists {
		return
	}
	c.seen[key] = struct{}{}
	c.descriptors = append(c.descriptors, dependencyDescriptor{Name: strings.Clone(artifact), Group: strings.Clone(group), Artifact: strings.Clone(artifact)})
}
