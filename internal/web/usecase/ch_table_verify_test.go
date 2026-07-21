package usecase

import (
	"context"
	"errors"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

type columnsReaderStub struct {
	cols      []domain.CHLogColumn
	found     bool
	err       error
	gotTable  string
	callCount int
}

func (s *columnsReaderStub) ReadTableColumns(_ context.Context, table string) ([]domain.CHLogColumn, bool, error) {
	s.callCount++
	s.gotTable = table
	return s.cols, s.found, s.err
}

func TestCHTableVerify_OK(t *testing.T) {
	t.Parallel()
	stub := &columnsReaderStub{cols: domain.RequiredLogColumns, found: true}
	uc := NewCHTableVerifyUsecase(stub, logging.NewNoop())

	res, err := uc.Verify(context.Background(), "external_db.audit_log")
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if !res.OK || res.TableMissing {
		t.Fatalf("want ok, got %+v", res)
	}
	if stub.gotTable != "external_db.audit_log" {
		t.Fatalf("table passed to reader: got %q", stub.gotTable)
	}
}

func TestCHTableVerify_TableMissing(t *testing.T) {
	t.Parallel()
	uc := NewCHTableVerifyUsecase(&columnsReaderStub{found: false}, logging.NewNoop())

	res, err := uc.Verify(context.Background(), "external_db.absent")
	if err != nil {
		t.Fatalf("отсутствие таблицы — результат проверки, а не ошибка вызова: %v", err)
	}
	if res.OK || !res.TableMissing {
		t.Fatalf("want table_missing, got %+v", res)
	}
}

func TestCHTableVerify_ReportsDiff(t *testing.T) {
	t.Parallel()
	// Убираем node_id и ломаем тип status.
	cols := make([]domain.CHLogColumn, 0, len(domain.RequiredLogColumns))
	for _, c := range domain.RequiredLogColumns {
		switch c.Name {
		case "node_id":
			continue
		case "status":
			c.Type = "String"
		}
		cols = append(cols, c)
	}
	uc := NewCHTableVerifyUsecase(&columnsReaderStub{cols: cols, found: true}, logging.NewNoop())

	res, err := uc.Verify(context.Background(), "external_db.audit_log")
	if err != nil {
		t.Fatalf("расхождения — не ошибка вызова: %v", err)
	}
	if res.OK {
		t.Fatal("want ok=false")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "node_id" {
		t.Fatalf("missing: got %v", res.Missing)
	}
	if len(res.Mismatched) != 1 || res.Mismatched[0].Name != "status" {
		t.Fatalf("mismatched: got %+v", res.Mismatched)
	}
}

func TestCHTableVerify_InvalidName(t *testing.T) {
	t.Parallel()
	stub := &columnsReaderStub{found: true}
	uc := NewCHTableVerifyUsecase(stub, logging.NewNoop())

	for _, table := range []string{"no_db", "db.a; DROP TABLE x--", "db.", ""} {
		if _, err := uc.Verify(context.Background(), table); !errors.Is(err, domain.ErrNodeClickHouseTableInvalid) {
			t.Fatalf("table %q: want ErrNodeClickHouseTableInvalid, got %v", table, err)
		}
	}
	if stub.callCount != 0 {
		t.Fatalf("кривое имя не должно доходить до ClickHouse, вызовов: %d", stub.callCount)
	}
}

func TestCHTableVerify_NoClickHouse(t *testing.T) {
	t.Parallel()
	uc := NewCHTableVerifyUsecase(nil, logging.NewNoop())

	if _, err := uc.Verify(context.Background(), "db.t"); !errors.Is(err, ErrCHUnavailable) {
		t.Fatalf("want ErrCHUnavailable, got %v", err)
	}
}
