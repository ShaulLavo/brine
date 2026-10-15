//go:build linux

package store

import (
 "testing"
 "strings"
 "github.com/ShaulLavo/brine/internal/datainit"
 "github.com/ShaulLavo/brine/internal/ops"
 "github.com/ShaulLavo/brine/internal/result"
)

func unstoppedPreparationFixture(t *testing.T) (*Store, *NativePreparation, ops.Operation) {
 t.Helper()
 ctx:=t.Context()
 s,p,deploy:=managedControlStateFixtureAtRoot(t,true,false,"")
 wanted:=commitAbandonFixtureRelease(t,s,p,deploy)
 requester,err:=datainit.ParseRequester("deploy:"+strings.Repeat("4",64));if err!=nil {t.Fatal(err)}
 ref,err:=s.SaveNativeTaskInput(ctx,NativeTaskInput{Requester:requester,Kind:ops.NativePrepare,BindingID:wanted.BindingID});if err!=nil {t.Fatal(err)}
 op,_,err:=s.CreateOperation(ctx,ops.Intent{Kind:ops.NativePrepare,App:p.App,SecretRef:ref},requester.String(),"unstopped-prepare");if err!=nil {t.Fatal(err)}
 if err=s.SetOperationState(ctx,op.ID,ops.Preflight);err!=nil {t.Fatal(err)}
 fence,err:=s.HoldDataFence(ctx,p.Managed.Database.DatabaseID,op.ID);if err!=nil {t.Fatal(err)}
 guard,err:=s.AcquireEnginePreparation(ctx,wanted.BindingID,fence.ID,op.ID);if err!=nil {t.Fatal(err)}
 t.Cleanup(func(){_ = guard.Close()})
 prep,err:=s.BeginNativePreparation(ctx,guard,op.ID);if err!=nil {t.Fatal(err)}
 routes:=NativePreparedRoutes{PointID:prep.PointID(),OperationID:op.ID,App:p.App,PreviousGeneration:1,PreviousFiles:map[string]string{p.App+".caddy":"sha256:"+strings.Repeat("b",64)},CurrentGeneration:2,CurrentFiles:map[string]string{}}
 if err=s.RecordNativePreparedRoutes(ctx,prep,routes,false);err!=nil {t.Fatal(err)}
 if err=s.RecordNativePreparedRoutes(ctx,prep,routes,true);err!=nil {t.Fatal(err)}
 if err=s.RecordNativePreparedFrontendStop(ctx,prep,false);err!=nil {t.Fatal(err)}
 if err=s.RecordNativePreparedFrontendStop(ctx,prep,true);err!=nil {t.Fatal(err)}
 if err=s.CompleteTask(ctx,op.ID,ops.RecoveryRequired,ops.TaskOutcome{Error:result.InternalError});err!=nil {t.Fatal(err)}
 if err=guard.Close();err!=nil {t.Fatal(err)}
 op,err=s.GetOperation(ctx,op.ID);if err!=nil {t.Fatal(err)}
 return s,prep,op
}

func TestNativePreparationBeforeEngineStopCanBeInspected(t *testing.T) {
 s,_,op:=unstoppedPreparationFixture(t)
 inspection,err:=s.OpenNativeBackupInspection(t.Context(),op.ID,op.Requester)
 if err!=nil {t.Fatal("preparation without an issued engine stop cannot be inspected",err)}
 defer func(){_ = inspection.Close()}()
}
