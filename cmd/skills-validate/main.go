// Command skills-validate checks every skill pack: frontmatter shape,
// the "Use when" trigger phrase, body limits, and description
// collisions — lexically always, and by embedding similarity when a
// gateway is reachable (GATEWAY_URL).
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
)

// similarityLimit: two descriptions at or above this cosine
// similarity are indistinguishable to the model picking a skill.
const similarityLimit = 0.75

func main() {
	dir := flag.String("dir", "skills", "skills directory to validate")
	flag.Parse()

	packs, err := skills.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}

	if err := lexicalCollisions(packs); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}

	if gw := os.Getenv("GATEWAY_URL"); gw != "" {
		switch err := embeddingCollisions(gw, packs); {
		case err == nil:
		case strings.Contains(err.Error(), "no_route"):
			// No embedding provider is enabled in this deployment;
			// the lexical check above still ran.
			fmt.Println("note: no embedding route configured; similarity check skipped")
		default:
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("note: GATEWAY_URL not set; embedding similarity check skipped")
	}

	fmt.Printf("ok: %d skills valid\n", len(packs))
}

func lexicalCollisions(packs []skills.Skill) error {
	seen := map[string]string{}
	for _, s := range packs {
		key := strings.ToLower(strings.Join(strings.Fields(s.Description), " "))
		if other, ok := seen[key]; ok {
			return fmt.Errorf("skills %s and %s have identical descriptions", other, s.Name)
		}
		seen[key] = s.Name
	}
	return nil
}

func embeddingCollisions(gatewayURL string, packs []skills.Skill) error {
	texts := make([]string, len(packs))
	for i, s := range packs {
		texts[i] = s.Description
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vecs, _, err := gwclient.New(gatewayURL).Embed(ctx, texts, "skills-validate")
	if err != nil {
		return fmt.Errorf("embedding check: %w", err)
	}
	for i := range packs {
		for j := i + 1; j < len(packs); j++ {
			if sim := cosine(vecs[i], vecs[j]); sim >= similarityLimit {
				return fmt.Errorf("skills %s and %s have near-identical descriptions (similarity %.2f >= %.2f)",
					packs[i].Name, packs[j].Name, sim, similarityLimit)
			}
		}
	}
	fmt.Println("embedding similarity check passed")
	return nil
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
