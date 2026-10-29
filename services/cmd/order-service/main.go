package main

import (
	"log"

	"gatekeeper/services/internal/echo"
)

func main() {
	if err := echo.Run("order-service"); err != nil {
		log.Fatal(err)
	}
}
