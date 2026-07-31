package config

import (
	"flag"
	"fmt"
	"os"
)

// Flags — флаги командной строки, общие для всех трёх бинарей.
type Flags struct {
	ConfigPath    string
	Debug         bool
	ShowVersion   bool
	MigrateUp     bool
	MigrateDownN  int
	MigrateStatus bool
	// MigrateForce — §74.4: объявить версию схемы и снять dirty. Строка, а не
	// int: «не задан» и «force 0» иначе неразличимы, а 0 — валидная цель.
	// Допустимо целое >= -1 (-1 = «миграций нет»).
	MigrateForce     string
	SetAdminPassword string // если задано — задаёт пароль admin'у и выходит
	// §70.5, аварийные обходы гейта владения ClickHouse.
	// CHAdopt — разрешить присвоить существующую БД без маркера владения
	// (PostgreSQL пересоздали, а ClickHouse остался). Чужой маркер не перебивает.
	// InstanceIDForce — однократно переписать сохранённый instance.id. БД в
	// ClickHouse при этом НЕ переименовываются — только руками.
	CHAdopt         bool
	InstanceIDForce bool
}

// ParseFlags парсит argv. Неизвестные флаги — error.
// При флаге --version печатает версию и завершает процесс.
func ParseFlags(version string) Flags {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var f Flags
	fs.StringVar(&f.ConfigPath, "config", "", "путь к YAML-конфигу (приоритет: флаг > $NEXUS_CONFIG > ./config/config.yml)")
	fs.StringVar(&f.ConfigPath, "c", "", "alias for --config")
	fs.BoolVar(&f.Debug, "debug", false, "загрузить config_debug.yml вместо config.yml")
	fs.BoolVar(&f.ShowVersion, "version", false, "напечатать версию и выйти")
	fs.BoolVar(&f.MigrateUp, "migrate-up", false, "применить все непримененные миграции и выйти")
	fs.IntVar(&f.MigrateDownN, "migrate-down", 0, "откатить N последних миграций и выйти")
	fs.BoolVar(&f.MigrateStatus, "migrate-status", false, "показать текущую версию схемы и выйти")
	fs.StringVar(&f.MigrateForce, "migrate-force", "", "§74.4: объявить версию схемы N и снять dirty БЕЗ выполнения SQL (-1 = миграций нет)")
	fs.StringVar(&f.SetAdminPassword, "set-admin-password", "", "задать пароль admin'у (bootstrap) и выйти")
	fs.BoolVar(&f.CHAdopt, "ch-adopt", false, "§70.5: присвоить существующие ClickHouse-БД без маркера владения (аварийный обход)")
	fs.BoolVar(&f.InstanceIDForce, "instance-id-force", false, "§70.5: однократно переписать сохранённый instance.id (БД в ClickHouse НЕ переименовываются)")

	_ = fs.Parse(os.Args[1:])

	if f.ShowVersion {
		fmt.Println(version)
		os.Exit(0)
	}
	return f
}
