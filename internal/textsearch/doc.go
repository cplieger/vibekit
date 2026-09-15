// Package textsearch is the substring match kernel every vibekit search
// surface scans with: one fold rule, one occurrence scan reporting positions
// into the original text, and the Tally a scan reports beside its matches.
//
// It imports nothing outside the standard library and nothing from vibekit, so
// it can be lifted into its own module unchanged.
package textsearch
