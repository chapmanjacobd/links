# Links

A Go-based port of `xklb` bookmarking and link-related tools.

## Features

- **links-add**: Add links to a SQLite database. Supports link extraction from webpages and paging.
- **links-open**: Open links from the database with fast substring search.

## Installation

```bash
go install github.com/chapmanjacobd/links
```

or

```bash
git clone https://github.com/chapmanjacobd/links/
cd links
make install
```

## Usage

### Adding Links

Add specific URLs:

```bash
./links add https://example.com https://google.com
```

Add and extract all links from a page:

```bash
./links add https://news.ycombinator.com
```

Add with a category:

```bash
./links add -c tech https://github.com
```

Paging:

```bash
./links add --max-pages 5 --page-key p https://example.com/search
```

### Opening Links

Open the most recent link matching a search term:
```bash
./links open google
```

Enable default regex sort:
```bash
./links open -R google
```

Open multiple links:
```bash
./links open -L 5 tech
```

Filter by category:
```bash
./links open -c tech
```

### Deleting Links

Delete links matching a search term:

```bash
./links open --delete-rows search_term
```

Delete all links in a category:

```bash
./links open --category tech --delete-rows --limit 1000
```

## Database

By default, links are stored in `links.db` in the current directory. You can specify a different path using `--db-path`.
