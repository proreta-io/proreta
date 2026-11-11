package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth/authdb"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// Audit actions of the platform admin's member management. They replace the customer-facing
// names (the shared code writes one row per change, never both).
const (
	ActionAdminMemberAdded       = "admin.member.added"
	ActionAdminMemberRoleChanged = "admin.member.role_changed"
	ActionAdminMemberRemoved     = "admin.member.removed"
	ActionAdminInvitationCreated = "admin.invitation.created"
	ActionAdminInvitationRevoked = "admin.invitation.revoked"
)

// ErrUserNotFound means no account matches the id or email.
var ErrUserNotFound = errors.New("auth: user not found")

// action is the audit action for a customer-facing action name: the admin.* name when the
// platform admin acts.
func (a Actor) action(customer string) string {
	if !a.platform {
		return customer
	}
	switch customer {
	case ActionMemberRoleChanged:
		return ActionAdminMemberRoleChanged
	case ActionMemberRemoved:
		return ActionAdminMemberRemoved
	case ActionMemberInvited:
		return ActionAdminInvitationCreated
	case ActionInvitationRevoked:
		return ActionAdminInvitationRevoked
	}
	return customer
}

// PlatformActor is a platform admin acting in an organization. The caller has checked the
// platform-admin flag; the actor then has the owner's rights there without being a member.
func PlatformActor(tenantID, userID uuid.UUID, ip netip.Addr) Actor {
	return Actor{TenantID: tenantID, UserID: userID, IP: ip, platform: true}
}

// FindUserByEmail returns the id of the account with this address (any status).
func (s *Service) FindUserByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	addr, msg := normalizeEmail(email)
	if msg != "" {
		return uuid.Nil, ErrUserNotFound
	}
	var id uuid.UUID
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := authdb.New(tx).GetUserByEmail(ctx, addr)
		id = u.ID
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUserNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("auth: find user: %w", err)
	}
	return id, nil
}

// SetMember makes an existing account a member with the role, or changes the role of a member.
// action is the audit action written, empty when nothing changed. The member quota counts
// open invitations and is never overridden; a disabled account can be added. The rules of
// ChangeRole apply (last owner, platform admin); the platform admin cannot join another
// organization. a must come from PlatformActor.
func (s *Service) SetMember(ctx context.Context, a Actor, userID uuid.UUID, role string) (m Member, action string, err error) {
	if !a.platform || !authz.Valid(authz.Role(role)) {
		return Member{}, "", authz.ErrForbidden
	}
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		actor, err := lockActorRole(ctx, tx, a)
		if err != nil {
			return err
		}
		q := authdb.New(tx)
		_, err = q.GetMembershipRole(ctx, authdb.GetMembershipRoleParams{TenantID: a.TenantID, UserID: userID})
		if err == nil {
			var changed bool
			m, changed, err = changeRole(ctx, tx, a, actor, userID, role)
			action = ""
			if changed {
				action = ActionAdminMemberRoleChanged
			}
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get membership: %w", err)
		}
		u, err := q.GetUserByID(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		if u.IsPlatformAdmin {
			// BootstrapAdmin refuses to start when the admin belongs to another tenant.
			return ErrSystemAdminMember
		}
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceMember); err != nil {
			return err
		}
		if err := q.InsertMembership(ctx, authdb.InsertMembershipParams{TenantID: a.TenantID, UserID: userID, Role: role}); err != nil {
			return fmt.Errorf("insert membership: %w", err)
		}
		row, err := q.LockMember(ctx, authdb.LockMemberParams{TenantID: a.TenantID, UserID: userID})
		if err != nil {
			return fmt.Errorf("read new member: %w", err)
		}
		m = Member{UserID: userID, Login: row.Email, DisplayName: row.DisplayName, Role: row.Role, JoinedAt: row.CreatedAt,
			IsPlatformAdmin: row.IsPlatformAdmin, Status: row.Status}
		action = ActionAdminMemberAdded
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: a.TenantID, ActorUserID: &a.UserID, Action: action,
			EntityType: "user", EntityID: userID.String(), IP: a.IP,
			Metadata: map[string]any{"role": role},
		})
	})
	if err != nil {
		var limit *plans.LimitError
		if errors.As(err, &limit) {
			return Member{}, "", err
		}
		return Member{}, "", wrapAdminMemberErr("set member", err)
	}
	return m, action, nil
}

// AdminRemoveMember is RemoveMember for the platform admin; a must come from PlatformActor.
func (s *Service) AdminRemoveMember(ctx context.Context, a Actor, userID uuid.UUID) error {
	if !a.platform {
		return authz.ErrForbidden
	}
	return s.RemoveMember(ctx, a, userID)
}

// AdminCreateInvitation is CreateInvitationFor for the platform admin; a must come from
// PlatformActor.
func (s *Service) AdminCreateInvitation(ctx context.Context, a Actor, in InvitationInput) (Invitation, string, bool, error) {
	if !a.platform {
		return Invitation{}, "", false, authz.ErrForbidden
	}
	return s.CreateInvitationFor(ctx, a, in)
}

// AdminRevokeInvitation is RevokeInvitation for the platform admin; revoked is false when the
// invitation was already closed. a must come from PlatformActor.
func (s *Service) AdminRevokeInvitation(ctx context.Context, a Actor, id uuid.UUID) (revoked bool, err error) {
	if !a.platform {
		return false, authz.ErrForbidden
	}
	return s.revokeInvitation(ctx, a, id)
}

func wrapAdminMemberErr(op string, err error) error {
	if errors.Is(err, ErrUserNotFound) {
		return err
	}
	return wrapMemberErr(op, err)
}
