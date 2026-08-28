package wanikani

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tomasmik/goi/internal/vocabulary"
)

const syncInterval = 12 * time.Hour

var (
	ErrNotConnected   = errors.New("WaniKani is not connected")
	ErrSyncInProgress = errors.New("WaniKani synchronization is already running")
)

type ServiceConfig struct {
	Client      *Client
	Credentials *Credentials
	Store       *Store
	Vocabulary  *vocabulary.Store
	Logger      *slog.Logger
	Interval    time.Duration
	Now         func() time.Time
}

type ServiceStatus struct {
	Status
	Connected bool
	Syncing   bool
}

type Service struct {
	client      *Client
	credentials *Credentials
	store       *Store
	vocabulary  *vocabulary.Store
	logger      *slog.Logger
	interval    time.Duration
	now         func() time.Time
	wake        chan struct{}
	busy        atomic.Bool
	operation   sync.Mutex
}

func NewService(config ServiceConfig) *Service {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Interval <= 0 {
		config.Interval = syncInterval
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{
		client:      config.Client,
		credentials: config.Credentials,
		store:       config.Store,
		vocabulary:  config.Vocabulary,
		logger:      config.Logger,
		interval:    config.Interval,
		now:         config.Now,
		wake:        make(chan struct{}, 1),
	}
}

func (s *Service) Status(ctx context.Context) (ServiceStatus, error) {
	_, connected, err := s.credentials.Load()
	if err != nil {
		return ServiceStatus{}, err
	}
	status, err := s.store.Status(ctx)
	if err != nil {
		return ServiceStatus{}, err
	}
	return ServiceStatus{Status: status, Connected: connected, Syncing: s.busy.Load()}, nil
}

func (s *Service) Connect(ctx context.Context, token string) (User, bool, error) {
	if !s.operation.TryLock() {
		return User{}, false, ErrSyncInProgress
	}
	defer s.operation.Unlock()
	token, err := validateToken(token)
	if err != nil {
		return User{}, false, err
	}
	user, err := s.client.User(ctx, token)
	if err != nil {
		return User{}, false, err
	}
	if err := s.credentials.Save(token); err != nil {
		return User{}, false, err
	}
	changed, err := s.store.ConfigureAccount(ctx, user)
	if err != nil {
		return User{}, false, err
	}
	s.RequestSync()
	return user, changed, nil
}

func (s *Service) Disconnect(ctx context.Context) error {
	if !s.operation.TryLock() {
		return ErrSyncInProgress
	}
	defer s.operation.Unlock()
	if err := s.credentials.Delete(); err != nil {
		return err
	}
	return s.store.Clear(ctx)
}

func (s *Service) RequestSync() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Sync(ctx context.Context) (returnErr error) {
	if !s.operation.TryLock() {
		return ErrSyncInProgress
	}
	defer s.operation.Unlock()
	s.busy.Store(true)
	defer s.busy.Store(false)
	startedAt := s.now().UTC()
	defer func() {
		if returnErr == nil || errors.Is(returnErr, ErrNotConnected) ||
			errors.Is(returnErr, context.Canceled) || errors.Is(returnErr, context.DeadlineExceeded) {
			return
		}
		if err := s.store.RecordFailure(ctx, startedAt, syncErrorMessage(returnErr)); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	token, connected, err := s.credentials.Load()
	if err != nil {
		return err
	}
	if !connected {
		return ErrNotConnected
	}
	user, err := s.client.User(ctx, token)
	if err != nil {
		return err
	}
	status, err := s.store.Status(ctx)
	if err != nil {
		return err
	}
	if status.UserID != user.ID {
		if _, err := s.store.ConfigureAccount(ctx, user); err != nil {
			return err
		}
		status, err = s.store.Status(ctx)
		if err != nil {
			return err
		}
	}

	updatedAfter := time.Time{}
	if !status.CursorAt.IsZero() {
		updatedAfter = status.CursorAt.Add(-time.Minute)
	}
	assignments, err := s.client.Assignments(ctx, token, updatedAfter)
	if err != nil {
		return err
	}
	type assignmentKey struct {
		typeName string
		level    int
	}
	assignmentBySubject := make(map[int64]assignmentKey, len(assignments))
	ids := make([]int64, 0, len(assignments))
	for _, assignment := range assignments {
		if !assignment.Started || assignment.Hidden ||
			(assignment.SubjectType != "vocabulary" && assignment.SubjectType != "kana_vocabulary") ||
			assignment.Level < 1 || assignment.Level > user.MaxLevelGranted {
			continue
		}
		if _, exists := assignmentBySubject[assignment.SubjectID]; exists {
			continue
		}
		assignmentBySubject[assignment.SubjectID] = assignmentKey{typeName: assignment.SubjectType, level: assignment.Level}
		ids = append(ids, assignment.SubjectID)
	}
	unseen, err := s.store.UnseenSubjectIDs(ctx, ids)
	if err != nil {
		return err
	}
	subjects, err := s.client.Subjects(ctx, token, unseen)
	if err != nil {
		return err
	}
	mappings := make([]SubjectMapping, 0, len(subjects))
	expressions := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		assignment, exists := assignmentBySubject[subject.ID]
		if !exists || subject.Type != assignment.typeName || subject.Level != assignment.level {
			return errors.New("WaniKani returned a subject that did not match its assignment")
		}
		if subject.Hidden || subject.Level > user.MaxLevelGranted {
			continue
		}
		mappings = append(mappings, SubjectMapping{ID: subject.ID, Expression: subject.Expression})
		expressions = append(expressions, subject.Expression)
	}
	if len(expressions) > 0 {
		if _, err := s.vocabulary.AddKnownExpressions(ctx, expressions); err != nil {
			return fmt.Errorf("import WaniKani vocabulary: %w", err)
		}
	}
	if err := s.store.CompleteSync(ctx, user, startedAt, s.now().UTC(), mappings); err != nil {
		return err
	}
	return nil
}

func (s *Service) Run(ctx context.Context) {
	if _, connected, err := s.credentials.Load(); err != nil {
		s.logger.Error("load WaniKani credential", "error", err)
	} else if connected {
		s.RequestSync()
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScheduled(ctx)
		case <-s.wake:
			s.runScheduled(ctx)
		}
	}
}

func (s *Service) runScheduled(ctx context.Context) {
	if err := s.Sync(ctx); err != nil && !errors.Is(err, ErrNotConnected) && !errors.Is(err, context.Canceled) {
		s.logger.Warn("WaniKani synchronization failed", "error", err)
	}
}

func syncErrorMessage(err error) string {
	if errors.Is(err, ErrAuthentication) {
		return "WaniKani rejected the saved token. Reconnect with a valid token."
	}
	return "Synchronization failed. Try again later."
}
