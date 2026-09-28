// Copyright 2026 The GitW3 Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hosting

import (
	"crypto/rand"
	"math/big"
)

// The word lists follow the spirit of Docker's container name generator:
// a friendly adjective followed by a notable scientist or engineer.
var nameAdjectives = []string{
	"admiring", "adoring", "affectionate", "amazing", "awesome", "blissful",
	"bold", "brave", "charming", "clever", "compassionate", "competent",
	"confident", "cool", "dazzling", "determined", "dreamy", "eager",
	"ecstatic", "elastic", "elated", "elegant", "eloquent", "epic",
	"exciting", "fervent", "festive", "flamboyant", "focused", "friendly",
	"frosty", "funny", "gallant", "gifted", "gracious", "happy",
	"hopeful", "inspiring", "intelligent", "jolly", "jovial", "keen",
	"kind", "laughing", "loving", "lucid", "magical", "modest",
	"musing", "nifty", "nostalgic", "optimistic", "peaceful", "pensive",
	"practical", "priceless", "quirky", "quizzical", "relaxed", "romantic",
	"serene", "sharp", "stoic", "sweet", "tender", "thirsty",
	"trusting", "upbeat", "vibrant", "vigilant", "vigorous", "wizardly",
	"wonderful", "youthful", "zealous", "zen",
}

var nameScientists = []string{
	"agnesi", "archimedes", "babbage", "bardeen", "bartik", "bell",
	"bhabha", "blackwell", "bohr", "bose", "brattain", "cannon",
	"carson", "cerf", "chandrasekhar", "clarke", "curie", "darwin",
	"dijkstra", "einstein", "elion", "engelbart", "euclid", "faraday",
	"fermi", "feynman", "franklin", "galileo", "gauss", "goldberg",
	"goodall", "hamilton", "hawking", "heisenberg", "hodgkin", "hopper",
	"hypatia", "jackson", "johnson", "kalam", "kepler", "khorana",
	"knuth", "lamarr", "leakey", "lovelace", "margulis", "maxwell",
	"mayer", "mcclintock", "meitner", "mendel", "mirzakhani", "morse",
	"newton", "noether", "pare", "pasteur", "payne", "perlman",
	"pike", "raman", "ramanujan", "ride", "ritchie", "rubin",
	"sammet", "shannon", "sinoussi", "tesla", "thompson", "torvalds",
	"turing", "villani", "wescoff", "wilson", "wozniak", "wright",
	"yalow", "yonath",
}

// RandomName returns a name like "admiring-lovelace" suitable as a DNS label.
func RandomName() string {
	return pick(nameAdjectives) + "-" + pick(nameScientists)
}

func pick(words []string) string {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		panic(err)
	}
	return words[index.Int64()]
}

// PoolFQDN returns the host name for a pool name under the base domain.
func PoolFQDN(name, baseDomain string) string {
	return name + "." + baseDomain
}
