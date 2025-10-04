package utils

import (
	"bufio"
	"log"
	"os"
)

func ReadFile(path string) []string {
	var data []string

	file, err := os.Open(path)

	if err != nil {
		log.Fatalf("Failed to open file %s \n", path)
	}

	defer file.Close()

	fileScanner := bufio.NewScanner(file)

	for fileScanner.Scan() {
		data = append(data, fileScanner.Text())
	}

	if err := fileScanner.Err(); err != nil {
		log.Fatalf("Error while reading file %s. Error: %s\n", path, err)
	}

	return data
}
