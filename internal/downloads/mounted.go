package downloads

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/themartz90/folderhug/internal/downloadfs"
)

func (s *Service) publishMounted(ctx context.Context, j job) error {
	prepared, err := s.stageMount(ctx, j)
	if err != nil {
		return mountedError(err)
	}
	defer prepared.Close()
	// Only the final link and state commit need to exclude queue actions.
	s.op.Lock()
	current, err := s.load(ctx, j.ID)
	if err == nil && current.State != "running" {
		err = context.Canceled
	}
	if err == nil {
		err = s.checkAuthority(ctx, current)
	}
	var name string
	if err == nil {
		name, err = prepared.Commit(ctx)
	}
	if err == nil {
		_, err = s.db.ExecContext(context.Background(), `UPDATE download_jobs SET state='completed',publishing=0,file_available=1,mounted_name=?,error=NULL,updated_at=? WHERE id=? AND state='running'`, name, time.Now().UnixMilli(), j.ID)
	}
	s.op.Unlock()
	if err != nil {
		return mountedError(err)
	}
	s.cleanupMountedStaging(j)
	if root, ok := s.mounts[j.DestinationID]; ok {
		if mount, err := openMountedRoot(root); err == nil {
			_ = mount.Cleanup(j.DestinationPath, j.ID)
			mount.Close()
		}
	}
	if completed, e := s.load(context.Background(), j.ID); e == nil {
		s.publish(completed)
	}
	return nil
}

func (s *Service) prepareMounted(ctx context.Context, j job) (*downloadfs.PreparedMount, error) {
	root, ok := s.mounts[j.DestinationID]
	if !ok {
		return nil, ErrDestinationUnavailable
	}
	internal, err := s.files.OpenJob(j.ID, false)
	if err != nil {
		return nil, err
	}
	defer internal.Close()
	src, err := internal.OpenCompleted(j.Name)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	hash, err := fileHash(ctx, src, j.TransferredBytes)
	if err != nil {
		return nil, err
	}
	if hash != j.checksum {
		return nil, ErrRestart
	}
	mount, err := openMountedRoot(root)
	if err != nil {
		return nil, ErrDestinationUnavailable
	}
	defer mount.Close()
	return mount.Prepare(ctx, j.DestinationPath, j.ID, j.Name, src, j.TransferredBytes)
}

func mountedError(err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return ErrBudget
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return ErrDestinationUnavailable
	}
	return err
}

func (s *Service) cleanupMountedStaging(j job) {
	dir, err := s.files.OpenJob(j.ID, false)
	if err != nil {
		return
	}
	defer dir.Close()
	_ = dir.RemoveCompleted(j.Name)
	_ = dir.RemovePart()
}
