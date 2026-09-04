"""A rowspan- and colspan-aware HTML table reader.

Google's documentation tables lean on rowspans — a metropolitan area written
once covers the entries beneath it — and its markup is not always well formed;
one row of the colocation table has no <tr> at all. A parser that walks rows and
counts cells therefore mis-attributes values silently, which is the one failure
a count check cannot catch: the enumeration comes out right and the associations
come out wrong.

This builds the grid a browser would build, carrying a spanned cell down and
across into every logical position it covers, so each row read out of it is the
row a reader sees.
"""

from html.parser import HTMLParser
import re


class TableCollector(HTMLParser):
    """Collects every table as rows of (text, rowspan, colspan)."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.tables = []
        self._table = None
        self._row = None
        self._cell = None
        self._span = None

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        if tag == 'table':
            self._table = []
        elif tag == 'tr' and self._table is not None:
            if self._row:
                self._table.append(self._row)
            self._row = []
        elif tag in ('td', 'th') and self._table is not None:
            if self._row is None:
                # A row whose <tr> the markup omits still starts a row here.
                self._row = []
            self._cell = []
            self._span = (int(attributes.get('rowspan', 1) or 1),
                          int(attributes.get('colspan', 1) or 1))

    def handle_data(self, data):
        if self._cell is not None:
            self._cell.append(data)

    def handle_endtag(self, tag):
        if tag in ('td', 'th') and self._cell is not None:
            text = re.sub(r'\s+', ' ', ''.join(self._cell)).strip()
            self._row.append((text,) + self._span)
            self._cell = None
            self._span = None
        elif tag == 'tr' and self._row is not None:
            self._table.append(self._row)
            self._row = None
        elif tag == 'table' and self._table is not None:
            if self._row:
                self._table.append(self._row)
                self._row = None
            self.tables.append(self._table)
            self._table = None


def expand(rows):
    """Expand spans into a rectangular grid of strings."""
    grid = []
    carried = {}
    for row in rows:
        line = []
        column = 0
        index = 0
        while True:
            while column in carried:
                text, remaining = carried[column]
                line.append(text)
                if remaining - 1:
                    carried[column] = (text, remaining - 1)
                else:
                    del carried[column]
                column += 1
            if index >= len(row):
                break
            text, rowspan, colspan = row[index]
            index += 1
            for _ in range(colspan):
                line.append(text)
                if rowspan > 1:
                    carried[column] = (text, rowspan - 1)
                column += 1
        if line:
            grid.append(line)
    return grid


def tables(html):
    """Every table in the document, as a grid of strings."""
    collector = TableCollector()
    collector.feed(html)
    return [expand(rows) for rows in collector.tables]
