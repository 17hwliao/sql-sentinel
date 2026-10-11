package verifierrpc

import (
	"context"
	"errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	workflow "sqlsentinel/internal/kafka"
	"sync"
	"time"
)

type progressKey struct{}

func Notify(ctx context.Context, phase string) {
	if notify, ok := ctx.Value(progressKey{}).(func(string)); ok {
		notify(phase)
	}
}

type Server struct {
	UnimplementedVerifierServer
	Get    func(context.Context, string) (workflow.Job, error)
	RunJob workflow.Runner
	mu     sync.Mutex
	active map[string]bool
	slots  chan struct{}
}

func NewServer(get func(context.Context, string) (workflow.Job, error), run workflow.Runner) *Server {
	return &Server{Get: get, RunJob: run, active: map[string]bool{}, slots: make(chan struct{}, 4)}
}
func (s *Server) Run(in *Request, stream grpc.ServerStreamingServer[Progress]) error {
	if len(in.JobId) < 24 || len(in.JobId) > 128 || len(in.LeaseOwner) == 0 || len(in.LeaseOwner) > 128 || in.Attempt < 1 {
		return status.Error(codes.InvalidArgument, "reference_invalid")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return status.Error(codes.ResourceExhausted, "verifier_busy")
	}
	s.mu.Lock()
	if s.active[in.JobId] {
		s.mu.Unlock()
		return status.Error(codes.AlreadyExists, "job_already_running")
	}
	s.active[in.JobId] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, in.JobId); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(stream.Context(), 2*time.Minute)
	defer cancel()
	job, e := s.Get(ctx, in.JobId)
	if e != nil || job.Status != workflow.Running || job.LeaseOwner != in.LeaseOwner || job.Attempts != int(in.Attempt) || !job.LeaseUntil.After(time.Now()) {
		return status.Error(codes.Aborted, "lease_invalid")
	}
	progress := make(chan string, 16)
	ctx = context.WithValue(ctx, progressKey{}, func(phase string) {
		select {
		case progress <- phase:
		default:
		}
	})
	if e = stream.Send(&Progress{Phase: "accepted"}); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- s.RunJob(ctx, job) }()
	for {
		select {
		case phase := <-progress:
			if e = stream.Send(&Progress{Phase: phase}); e != nil {
				cancel()
				<-done
				return e
			}
		case e = <-done:
			if errors.Is(e, workflow.ErrRejected) {
				return status.Error(codes.FailedPrecondition, "job_rejected")
			}
			if errors.Is(e, workflow.ErrLeaseLost) {
				return status.Error(codes.Aborted, "lease_lost")
			}
			if e != nil {
				if ctx.Err() != nil {
					return status.FromContextError(ctx.Err()).Err()
				}
				return status.Error(codes.Unavailable, "verification_failed")
			}
			return stream.Send(&Progress{Phase: "completed", Completed: true})
		case <-ctx.Done():
			cancel()
			<-done
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}
func Client(conn grpc.ClientConnInterface, observe func(string)) workflow.Runner {
	client := NewVerifierClient(conn)
	return func(ctx context.Context, job workflow.Job) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		stream, e := client.Run(ctx, &Request{JobId: job.ID, LeaseOwner: job.LeaseOwner, Attempt: int32(job.Attempts)})
		if e != nil {
			return e
		}
		for {
			progress, e := stream.Recv()
			if e != nil {
				if status.Code(e) == codes.FailedPrecondition {
					return workflow.ErrRejected
				}
				if status.Code(e) == codes.Aborted {
					return workflow.ErrLeaseLost
				}
				if errors.Is(e, io.EOF) {
					return errors.New("verifier_truncated")
				}
				return errors.New("verifier_unavailable")
			}
			if observe != nil {
				observe(progress.Phase)
			}
			if progress.Completed {
				return nil
			}
		}
	}
}
