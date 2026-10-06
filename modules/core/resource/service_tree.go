package resource

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (s *Service) Tree(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) ([]Node, error) {
	if err := validateContext(ctx, "resource tree"); err != nil {
		return nil, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	if siteID <= 0 {
		return nil, errors.New("resource site id is invalid")
	}

	siteRuntime, exists := s.runtime(ctx, siteID)
	if !exists {
		return nil, fmt.Errorf("resource site %d not found", siteID)
	}

	items, err := s.repository.ListBySite(ctx, siteID)
	if err != nil {
		return nil, fmt.Errorf("list resources for site %d: %w", siteID, err)
	}

	rawByID := make(map[ID]Resource, len(items))
	for index, item := range items {
		if item.ID <= 0 {
			return nil, fmt.Errorf(
				"resource at index %d has invalid id",
				index,
			)
		}
		if item.SiteID != siteID {
			return nil, fmt.Errorf(
				"resource %d belongs to site %d instead of %d",
				item.ID,
				item.SiteID,
				siteID,
			)
		}
		if _, exists := rawByID[item.ID]; exists {
			return nil, fmt.Errorf(
				"duplicate resource id %d",
				item.ID,
			)
		}
		rawByID[item.ID] = item
	}

	normalized := make([]Resource, 0, len(items))
	for _, item := range items {
		storedPath := cloneString(item.Path)
		result, err := s.normalize(
			ctx,
			security.System(),
			item,
			siteRuntime,
			rawByID,
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"validate stored resource %d: %w",
				item.ID,
				err,
			)
		}
		if !equalStrings(storedPath, result.Path) {
			return nil, fmt.Errorf(
				"validate stored resource %d: stored path is inconsistent",
				item.ID,
			)
		}
		normalized = append(normalized, result)
	}

	return buildTree(normalized)
}

func (s *Service) ensureNoParentCycle(
	ctx context.Context,
	item Resource,
) error {
	if item.ParentID == nil {
		return nil
	}

	visited := map[ID]struct{}{item.ID: {}}
	currentID := cloneID(item.ParentID)
	for currentID != nil {
		if _, exists := visited[*currentID]; exists {
			return ErrInvalidTree
		}
		visited[*currentID] = struct{}{}

		current, err := s.repository.ByID(ctx, *currentID)
		if err != nil {
			return fmt.Errorf(
				"walk resource parent %d: %w",
				*currentID,
				err,
			)
		}
		if current.SiteID != item.SiteID {
			return errors.New("resource parent belongs to another site")
		}
		currentID = cloneID(current.ParentID)
	}

	return nil
}

func (s *Service) ensureNoRouteDescendants(
	ctx context.Context,
	item Resource,
	siteRuntime *site.Runtime,
) error {
	items, err := s.repository.ListBySite(ctx, item.SiteID)
	if err != nil {
		return fmt.Errorf(
			"list descendants of resource %d: %w",
			item.ID,
			err,
		)
	}

	byID := make(map[ID]Resource, len(items))
	for _, candidate := range items {
		byID[candidate.ID] = candidate
	}

	for _, candidate := range items {
		if candidate.ID == item.ID {
			continue
		}

		visited := make(map[ID]struct{})
		parentID := cloneID(candidate.ParentID)
		isDescendant := false
		for parentID != nil {
			if *parentID == item.ID {
				isDescendant = true
				break
			}
			if _, exists := visited[*parentID]; exists {
				return ErrInvalidTree
			}
			visited[*parentID] = struct{}{}

			parent, exists := byID[*parentID]
			if !exists {
				return fmt.Errorf(
					"resource %d references missing parent %d",
					candidate.ID,
					*parentID,
				)
			}
			parentID = cloneID(parent.ParentID)
		}
		if !isDescendant {
			continue
		}

		resourceType, exists := siteRuntime.Profile().
			Registry().
			ResourceType(candidate.Type)
		if !exists {
			return fmt.Errorf(
				"resource descendant %d references unknown type %q",
				candidate.ID,
				candidate.Type,
			)
		}
		if resourceType.PathMode() == resourcetype.PathRoute {
			return errors.New(
				"resource with route descendants cannot use no_path type",
			)
		}
	}

	return nil
}

func buildTree(items []Resource) ([]Node, error) {
	type mutableNode struct {
		resource Resource
		children []*mutableNode
	}

	nodes := make(map[ID]*mutableNode, len(items))
	for _, item := range items {
		if _, exists := nodes[item.ID]; exists {
			return nil, fmt.Errorf("duplicate resource id %d", item.ID)
		}
		nodes[item.ID] = &mutableNode{resource: Clone(item)}
	}

	roots := make([]*mutableNode, 0)
	for _, item := range items {
		node := nodes[item.ID]
		if item.ParentID == nil {
			roots = append(roots, node)
			continue
		}

		parent, exists := nodes[*item.ParentID]
		if !exists {
			return nil, fmt.Errorf(
				"resource %d references missing parent %d",
				item.ID,
				*item.ParentID,
			)
		}
		parent.children = append(parent.children, node)
	}

	sortNodes := func(nodes []*mutableNode) {
		sort.Slice(nodes, func(left, right int) bool {
			if nodes[left].resource.Sort != nodes[right].resource.Sort {
				return nodes[left].resource.Sort <
					nodes[right].resource.Sort
			}
			return nodes[left].resource.ID < nodes[right].resource.ID
		})
	}
	sortNodes(roots)
	for _, node := range nodes {
		sortNodes(node.children)
	}

	state := make(map[ID]uint8, len(nodes))
	visited := 0
	var convert func(*mutableNode) (Node, error)
	convert = func(current *mutableNode) (Node, error) {
		switch state[current.resource.ID] {
		case 1:
			return Node{}, ErrInvalidTree
		case 2:
			return Node{}, ErrInvalidTree
		}

		state[current.resource.ID] = 1
		result := Node{Resource: Clone(current.resource)}
		for _, child := range current.children {
			converted, err := convert(child)
			if err != nil {
				return Node{}, err
			}
			result.Children = append(result.Children, converted)
		}
		state[current.resource.ID] = 2
		visited++
		return result, nil
	}

	result := make([]Node, 0, len(roots))
	for _, root := range roots {
		converted, err := convert(root)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	if visited != len(nodes) {
		return nil, ErrInvalidTree
	}

	return result, nil
}
