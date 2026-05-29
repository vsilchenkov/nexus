package port

import "context"

// UnitOfWork — атомарный блок над несколькими репозиториями (§17.4 ТЗ).
//
// Реализация (Postgres) начинает транзакцию, оборачивает все репо в
// транзакционные версии, вызывает fn с этими репо и коммитит при успехе
// (откат при ошибке). Usecase не знает про BEGIN/COMMIT — только про факт
// «эти операции должны быть атомарны».
type UnitOfWork interface {
	Execute(ctx context.Context, fn func(ctx context.Context, repos Repos) error) error
}

// Repos — bag репозиториев, доступных внутри транзакционного блока.
type Repos struct {
	Nodes NodeRepo
	Audit AuditRepo
	Hosts HostAllowlistRepo
}
