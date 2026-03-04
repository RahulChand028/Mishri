package main

import (
	"context"
	"fmt"

	"github.com/rahul/mishri/internal/tools"
)

func main() {
	skills, err := tools.LoadSkills("workspace/skills")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	if len(skills) == 0 {
		fmt.Println("No skills loaded")
		return
	}

	skill := skills[0]
	fmt.Printf("Loaded: %s - %s\n", skill.Name(), skill.Description())

	out, err := skill.Execute(context.Background(), `{"city": "Khatima"}`)
	if err != nil {
		fmt.Printf("Execution Error: %v\n", err)
		return
	}

	fmt.Printf("Output: %s\n", out)
}
