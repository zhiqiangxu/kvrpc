package ddl

import (
	"context"
	"fmt"
	"time"

	"github.com/zhiqiangxu/mondis"
	"github.com/zhiqiangxu/mondis/document/config"
	"github.com/zhiqiangxu/mondis/document/dml"
	"github.com/zhiqiangxu/mondis/document/meta"
	"github.com/zhiqiangxu/mondis/document/model"
	"github.com/zhiqiangxu/mondis/util"
	util2 "github.com/zhiqiangxu/util"
	"github.com/zhiqiangxu/util/logger"
	"github.com/zhiqiangxu/util/osc"
	"go.uber.org/zap"
)

type workerType byte

const (
	defaultWorkerType workerType = 0
)

type worker struct {
	tp    workerType
	jobCh chan struct{}
	d     *DDL
}

func newWorker(tp workerType, d *DDL) *worker {
	return &worker{tp: tp, jobCh: make(chan struct{}), d: d}
}

func (w *worker) start() {
	conf := config.Load()
	workerCheckTime := util.ChooseTime(2*conf.Lease, conf.WorkerMaxTickInterval)

	ticker := time.NewTicker(workerCheckTime)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case <-w.jobCh:
		}

		err := w.handleJobQueue()
		if err != nil {
			logger.Instance().Error("handleJobQueue", zap.Error(err))
		}
	}
}

const (
	jobMaxErrorCount = 3
)

// handleJobQueue handles jobs in Job queue.
func (w *worker) handleJobQueue() (err error) {

	var (
		nojob         bool
		schemaVersion int64
		failNow       bool
		runJobErr     error
		cancelFunc    func()
		job           *model.Job
	)
	for {
		err = util.RunInNewUpdateTxnWithCancel(w.d.kvdb, func(txn mondis.ProviderTxn) (err error) {
			m := meta.NewMeta(txn)

			job, err = w.getFirstJob(m)
			if err != nil {
				return
			}
			if job == nil {
				nojob = true
				return
			}

			if job.IsDone() || job.IsRollbackDone() {
				if !job.IsRollbackDone() {
					job.State = model.JobStateSynced
				}
				err = w.finishJob(m, job)
				return
			}

			util2.RunWithRecovery(func() {
				schemaVersion, cancelFunc, failNow, runJobErr = w.runJob(m, job)
			}, func(interface{}) {
				job.State = model.JobStateCancelling
			})

			if runJobErr != nil {
				job.ErrorCount++
				job.Error = runJobErr
				logger.Instance().Error("runJob", zap.Any("job", job), zap.Error(runJobErr))
				if failNow || job.ErrorCount >= jobMaxErrorCount {
					err = w.finishJob(m, job)
					return
				}
			}

			if job.IsCancelled() {
				err = w.finishJob(m, job)
				return
			}

			err = w.updateJob(m, job)
			return
		}, func() {
			cancelFunc()
		})
		if nojob {
			return
		}
		if w.d.options.Callback.OnChanged != nil {
			changeErr := runJobErr
			if changeErr == nil {
				changeErr = err
			}
			w.d.options.Callback.OnChanged(changeErr)
		}
		if err != nil {
			return
		}

		if runJobErr != nil {
			time.Sleep(time.Second)
		}

		w.waitSchemaChanged(schemaVersion, job)
	}
}

func (w *worker) runJob(m *meta.Meta, job *model.Job) (schemaVersion int64, cancelFunc func(), failNow bool, err error) {
	if job.IsFinished() {
		return
	}

	switch job.Type {
	case model.ActionCreateSchema:
		schemaVersion, cancelFunc, failNow, err = w.onCreateSchema(m, job)
	default:
		// Invalid job, cancel it.
		job.State = model.JobStateCancelled
		err = fmt.Errorf("invalid ddl job type: %v", job.Type)
	}
	return
}

func (w *worker) updateJob(m *meta.Meta, job *model.Job) (err error) {
	err = m.UpdateDDLJob(0, job)
	return
}

func (w *worker) finishJob(m *meta.Meta, job *model.Job) (err error) {

	_, err = m.DeQueueDDLJob()
	if err != nil {
		return
	}

	err = m.AddHistoryDDLJob(job)
	return
}

func (w *worker) getFirstJob(m *meta.Meta) (job *model.Job, err error) {
	job, err = m.GetDDLJobByIdx(0)
	return
}

func (w *worker) onCreateSchema(m *meta.Meta, job *model.Job) (schemaVersion int64, cancelFunc func(), failNow bool, err error) {

	dbInfo := &model.DBInfo{}
	if err = job.DecodeArg(dbInfo); err != nil {
		job.State = model.JobStateCancelled
		return
	}

	exists, err := checkDBNameNotExists(m, dbInfo.Name)
	if err != nil {
		return
	}
	if exists {
		failNow = true
		err = ErrDBAlreadyExists
		return
	}
	cf := func() {
		for _, collection := range dbInfo.Collections {
			util2.TryUntilSuccess(func() bool {
				dropErr := dml.DropSequenceIfExists(collection.ID)
				if dropErr != nil {
					logger.Instance().Error("DropSequenceIfExists", zap.Int64("cid", collection.ID), zap.Error(dropErr))
				}
				return dropErr == nil
			}, time.Second)
		}
	}
	defer func() {
		if err != nil {
			cf()
		}
	}()

	dbInfo.State = osc.StatePublic
	for _, collection := range dbInfo.Collections {
		err = dml.CreateSequence(w.d.kvdb, dbInfo.ID, collection.ID, 0)
		if err != nil {
			return
		}
		collection.State = osc.StatePublic
		for _, index := range collection.Indices {
			index.State = osc.StatePublic
		}
	}
	err = m.CreateDatabase(dbInfo)
	if err != nil {
		return
	}
	for _, collection := range dbInfo.Collections {
		err = m.CreateCollection(dbInfo.ID, collection)
		if err != nil {
			return
		}
	}

	schemaVersion, err = w.d.updateSchemaVersion(m, job)
	if err != nil {
		return
	}
	job.FinishDBJob(model.JobStateDone, osc.StatePublic, schemaVersion, dbInfo)

	cancelFunc = cf
	return

}

// updateSchemaVersion increments the schema version by 1 and sets SchemaDiff.
func (d *DDL) updateSchemaVersion(m *meta.Meta, job *model.Job) (schemaVersion int64, err error) {
	schemaVersion, err = m.GenSchemaVersion()
	if err != nil {
		return
	}

	diff := &model.SchemaDiff{
		Version:       schemaVersion,
		Type:          job.Type,
		CollectionIDs: job2CollectionIDs(job),
		Arg:           job.Arg,
		RawArg:        job.RawArg,
	}

	err = m.SetSchemaDiff(diff)
	return
}

func (d *DDL) checkJob(ctx context.Context, job *model.Job) (err error) {
	// For a job from start to end, the state of it will be none -> delete only -> write only -> reorganization -> public
	// For every state changes, we will wait as lease 2 * lease time, so here the ticker check is 10 * lease.
	ticker := time.NewTicker(util.ChooseTime(10*config.Load().Lease, checkJobMaxInterval(job.Type)))
	defer ticker.Stop()

	var historyJob *model.Job
	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			err = ctx.Err()
			return
		}

		historyJob, err = d.GetHistoryJob(job.ID)
		if err != nil {
			logger.Instance().Error("GetHistoryJob", zap.Error(err))
			continue
		} else if historyJob == nil {
			logger.Instance().Debug("job not in history", zap.Int64("jobID", job.ID))
			continue
		}

		if historyJob.IsSynced() {
			return
		}

		if historyJob.Error != nil {
			err = historyJob.Error
			return
		}

		panic("When the state is JobStateRollbackDone or JobStateCancelled, historyJob.Error should never be nil")
	}

}

func checkJobMaxInterval(jobTp model.ActionType) time.Duration {
	// The job of adding index takes more time to process.
	// So it uses the longer time.
	if jobTp == model.ActionAddIndex {
		return 3 * time.Second
	}
	if jobTp == model.ActionCreateCollection || jobTp == model.ActionCreateSchema {
		return 500 * time.Millisecond
	}
	return 1 * time.Second
}

func (d *DDL) notifyWorker(jobTp model.ActionType) {
	select {
	case d.workers[defaultWorkerType].jobCh <- struct{}{}:
	default:
	}
}

func (w *worker) waitSchemaChanged(schemaVersion int64, job *model.Job) {
	lease := config.Load().Lease
	if lease == 0 {
		return
	}

	time.Sleep(2 * lease)
}

func job2CollectionIDs(job *model.Job) (collectionIDs []int64) {
	switch job.Type {
	case model.ActionCreateSchema:
		dbInfo := job.Arg.(*model.DBInfo)
		for _, c := range dbInfo.Collections {
			collectionIDs = append(collectionIDs, c.ID)
		}
	default:
	}
	return
}
