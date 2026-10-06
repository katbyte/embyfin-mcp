// Package naming reads what a file's name, a release's name and a title say,
// and compares titles, with no server in sight.
//
// A path is the one piece of metadata the server did not write: the title,
// the year, the season and the episode in it were put there by whoever
// placed the file. A release name says the same things the scene's way, dots
// for spaces and the encode after the title. And a title is spelled many ways
// by the people and the providers that write it: with and without its
// article, a number in words or in digits, a part written as "Part Two", "Pt.
// 2", "(2)" or "II", a colon dropped, an accent folded. The tools that judge
// a library against its files, resolve a release to a series, or tell two
// entries apart all read names through here, so one rule holds everywhere.
//
// The package is pure: it reads strings and answers with strings, numbers and
// verdicts. What a server holds for an item, and what TMDB knows of it, is
// the tools' and sdk/tmdb's to bring.
package naming
