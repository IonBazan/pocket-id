package sessionrevocation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/italypaleale/francis/actor"
	"github.com/lestrrat-go/jwx/v4/jwt"
	"gorm.io/gorm"

	"github.com/pocket-id/pocket-id/backend/internal/model"
	datatype "github.com/pocket-id/pocket-id/backend/internal/model/types"
)

// Session tokens are stateless, so they are revoked in two ways
// A single token is revoked by storing its "jti" in the actor state store, with a TTL that ends when the token expires, so no cleanup job is needed
// All tokens of a user are revoked by moving the user's sessions_valid_after cutoff forward

// revokedActorType is the actor type under which revoked token IDs are stored
// No actor is registered for it, because the state is only read and written directly
const revokedActorType = "RevokedSession"

// StateStore is the subset of the actor service used to store revoked token IDs
type StateStore interface {
	SetState(ctx context.Context, actorType string, actorID string, state any, opts *actor.SetStateOpts) error
	GetState(ctx context.Context, actorType string, actorID string, dest any) error
}

type revokedState struct {
	RevokedAt time.Time
}

// RevokeToken revokes a single session token until it expires
func RevokeToken(ctx context.Context, store StateStore, token jwt.Token) error {
	jti, ok := token.JwtID()
	if !ok {
		return errors.New("session token does not contain a jti claim")
	}

	// An expired token is already rejected, so there is nothing to store
	exp, _ := token.Expiration()
	ttl := time.Until(exp)
	if ttl <= 0 {
		return nil
	}

	err := store.SetState(ctx, revokedActorType, jti, revokedState{RevokedAt: time.Now()}, &actor.SetStateOpts{TTL: ttl})
	if err != nil {
		return fmt.Errorf("error storing revoked session: %w", err)
	}

	return nil
}

// IsRevoked reports whether the token was revoked on its own or was issued before the user's cutoff
func IsRevoked(ctx context.Context, store StateStore, token jwt.Token, user model.User) (bool, error) {
	// JWT timestamps have second precision, so tokens issued in the same second as the cutoff stay valid
	issuedAt, _ := token.IssuedAt()
	if user.SessionsValidAfter != nil && issuedAt.Before(user.SessionsValidAfter.ToTime().Truncate(time.Second)) {
		return true, nil
	}

	jti, ok := token.JwtID()
	if !ok {
		return true, nil
	}

	var state revokedState
	err := store.GetState(ctx, revokedActorType, jti, &state)
	if errors.Is(err, actor.ErrStateNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("error loading revoked session: %w", err)
	}

	return true, nil
}

// RevokeAllForUser revokes every session token issued to the user until now
func RevokeAllForUser(ctx context.Context, tx *gorm.DB, userID string) error {
	err := tx.
		WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", userID).
		Update("sessions_valid_after", datatype.DateTime(time.Now())).
		Error
	if err != nil {
		return fmt.Errorf("error revoking sessions: %w", err)
	}

	return nil
}
