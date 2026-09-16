package kernel

import "context"

type ConnectionCode string

type DBConnector interface {
	Code() ConnectionCode
	Ping(ctx context.Context) error
	Close() error
}

type ConnectorFactory interface {
	Code() ConnectionCode
	Open(context.Context) (DBConnector, error)
}

type ModuleDatabase interface {
	ModuleCode() ModuleCode
}

type ModuleDatabaseFactory interface {
	ModuleCode() ModuleCode
	Build(DBConnector) (ModuleDatabase, error)
}

type DatabaseResolver interface {
	MainModuleDatabase(ModuleCode) (ModuleDatabase, bool)
	ModuleDatabase(ConnectionCode, ModuleCode) (ModuleDatabase, bool)
}
