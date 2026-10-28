package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gophermart/internal/app"
)

func main() {
	os.Exit(run())
}

// run выполняет запуск сервиса и возвращает код завершения процесса.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Main(ctx, os.Args[1:], os.LookupEnv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gophermart:", err)

		return 1
	}

	return 0
}
