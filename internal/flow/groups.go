package flow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

var (
	// ErrOperatorsGroup is a change to who is in the operators group, which
	// the deployment's configuration owns.
	ErrOperatorsGroup = errors.New("the operators group can only be changed in the Paisan Stack deployment configuration")
	// ErrNoSuchGroup is a change naming a group the identity provider does not have.
	ErrNoSuchGroup = errors.New("that group no longer exists; reload the page")
	// ErrNoGroupChange is a save that adds and removes nothing.
	ErrNoGroupChange = errors.New("no group was changed")
)

// GroupRow is one of the identity provider's groups on a user's groups page.
type GroupRow struct {
	idp.Group
	// In is whether the user is in the group; Locked, that the page may not
	// change it.
	In, Locked bool
}

// GroupsPage is a user's groups page.
type GroupsPage struct {
	Member MemberRow
	Groups []GroupRow
	// Operator is a user in the operators group or named in
	// ACCOUNT_OPERATORS: the next paisans deployment apply restores their groups.
	Operator bool
}

// MemberGroups is every group, with the user's own marked.
func (s *Service) MemberGroups(ctx context.Context, admin Member, userID string) (GroupsPage, error) {
	row, err := s.MemberByID(ctx, admin, userID)
	if err != nil {
		return GroupsPage{}, err
	}
	all, err := s.p.ListGroups(ctx)
	if err != nil {
		return GroupsPage{}, err
	}
	mine, err := s.p.UserGroups(ctx, userID)
	if errors.Is(err, idp.ErrNotFound) {
		return GroupsPage{}, store.ErrNotFound
	}
	if err != nil {
		return GroupsPage{}, err
	}
	p := GroupsPage{Member: row, Operator: row.Operator}
	for _, g := range all {
		in := slices.ContainsFunc(mine, func(m idp.Group) bool { return m.ID == g.ID })
		locked := g.Name == idp.OperatorsGroup
		if locked && in {
			p.Operator = true
		}
		p.Groups = append(p.Groups, GroupRow{Group: g, In: in, Locked: locked})
	}
	return p, nil
}

// ids trims each ID and drops blanks and repeats.
func ids(list []string) []string {
	var out []string
	for _, id := range list {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func groupNames(gs []idp.Group) string {
	names := make([]string, 0, len(gs))
	for _, g := range gs {
		names = append(names, g.Name)
	}
	return strings.Join(names, ",")
}

// ChangeGroups adds the user to the groups whose IDs are in add and takes
// them out of those in remove, leaving every other group as the identity
// provider has it at the moment of writing. Any admin may change any user's
// groups, their own and other admins' included, except the operators group.
// The audit entry records what the write actually added and removed.
func (s *Service) ChangeGroups(ctx context.Context, admin Member, userID string, add, remove []string) (idp.GroupChange, error) {
	if !admin.Admin {
		return idp.GroupChange{}, ErrForbidden
	}
	add, remove = ids(add), ids(remove)
	if len(add)+len(remove) == 0 {
		return idp.GroupChange{}, ErrNoGroupChange
	}
	all, err := s.p.ListGroups(ctx)
	if err != nil {
		return idp.GroupChange{}, err
	}
	for _, id := range append(slices.Clone(add), remove...) {
		i := slices.IndexFunc(all, func(g idp.Group) bool { return g.ID == id })
		switch {
		case i < 0:
			return idp.GroupChange{}, ErrNoSuchGroup
		case all[i].Name == idp.OperatorsGroup:
			return idp.GroupChange{}, ErrOperatorsGroup
		}
	}
	ch, err := s.p.ChangeGroups(ctx, userID, add, remove)
	switch {
	case errors.Is(err, idp.ErrOperatorsGroup):
		return idp.GroupChange{}, ErrOperatorsGroup
	case errors.Is(err, idp.ErrNoSuchGroup):
		return idp.GroupChange{}, ErrNoSuchGroup
	case errors.Is(err, idp.ErrNotFound):
		return idp.GroupChange{}, store.ErrNotFound
	}
	// From here the groups may have changed: what follows records it, and
	// must not be cut short by the request going away.
	actx := context.WithoutCancel(ctx)
	subject := "user=" + userID + " added=" + groupNames(ch.Added) + " removed=" + groupNames(ch.Removed)
	if errors.Is(err, idp.ErrUnconfirmed) {
		if aerr := s.st.Audit(actx, admin.Sub, "groups.change_unconfirmed", subject); aerr != nil {
			slog.Error("an unconfirmed group change could not be audited", "user_id", userID, "error", aerr)
		}
		return ch, err
	}
	if err != nil {
		return idp.GroupChange{}, err
	}
	if err := s.st.Audit(actx, admin.Sub, "groups.change", subject); err != nil {
		slog.Error("a group change could not be audited", "user_id", userID, "error", err)
		return ch, fmt.Errorf("the groups are changed but the audit entry could not be written: %w", err)
	}
	slog.Info("write", "action", "groups.change", "user_id", userID)
	return ch, nil
}
