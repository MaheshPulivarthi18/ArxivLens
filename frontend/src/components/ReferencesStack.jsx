import React from 'react';
import { ExternalLink, FileCode, ShieldCheck, ShieldAlert, Info, BookOpen } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent, CardFooter } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';
import { Tooltip, TooltipTrigger, TooltipContent } from './ui/tooltip';
import { citationState, groupByMarker } from '../lib/citations';

const CHIP_ICONS = { supported: ShieldCheck, unsupported: ShieldAlert, unchecked: Info };

// `active` = { marker, pos } | null: the card the reader came to from a marker (design FE-4 §4). Ignored in refusal
// mode, which has no markers. Cards are grouped by marker (one per passage) and are focus targets, not Tab stops.
export default function ReferencesStack({ citations, active, traceId, onCopy, refusalReason }) {
  if (!citations || citations.length === 0) {
    return (
      <Card className="border-border/60 bg-card/60 p-6 text-center text-muted-foreground text-sm">
        <BookOpen className="w-8 h-8 mx-auto mb-2 opacity-40" />
        <p>No reference papers cited for this query.</p>
      </Card>
    );
  }

  const handleBibTeXCopy = (cit) => {
    const cleanID = cit.arxiv_id ? cit.arxiv_id.replace(/[^a-zA-Z0-9]/g, '') : 'arxivpaper';
    const bibtex = `@article{arxiv_${cleanID},
  title = {${cit.title}},
  eprint = {${cit.arxiv_id}},
  archivePrefix = {arXiv},
  primaryClass = {cs.AI},
  url = {https://arxiv.org/abs/${cit.arxiv_id}},
  year = {2026}
}`;
    onCopy(bibtex, `📋 BibTeX reference copied for [${cit.marker}]!`);
  };

  const groups = groupByMarker(citations);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between px-1">
        <h3
          id="references-heading"
          tabIndex={-1}
          className="text-sm font-semibold text-foreground flex items-center gap-2 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span>{refusalReason ? 'Retrieved passages (no answer was shown)' : 'References'}</span>
          <Badge variant="secondary" className="text-xs px-2 py-0">
            {groups.length} {refusalReason ? (groups.length === 1 ? 'passage' : 'passages') : 'cited'}
          </Badge>
        </h3>
        {!refusalReason && <span className="text-xs text-muted-foreground">Click [n] in text to inspect</span>}
      </div>

      {groups.map(({ marker, entries }) => {
        const cit = entries[0].citation;
        const isHighlighted = !refusalReason && active?.marker === marker;
        const pdfURL = cit.pdf_url || `https://arxiv.org/pdf/${cit.arxiv_id}.pdf`;

        return (
          <Card
            key={`${traceId}-${marker}`}
            id={`ref-${marker}`}
            role="article"
            aria-label={`Citation [${cit.marker}]: ${cit.title}`}
            tabIndex={-1}
            data-active={isHighlighted ? 'true' : undefined}
            className={`transition-all duration-300 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              isHighlighted
                ? 'border-primary ring-2 ring-ring/30 bg-card shadow-lg'
                : 'border-border/70 hover:border-border bg-card/90'
            }`}
          >
            <CardHeader className="p-4 pb-2">
              <div className="flex items-start justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span className="flex items-center justify-center w-6 h-6 rounded-md bg-accent text-accent-foreground font-mono font-bold text-xs">
                    [{cit.marker}]
                  </span>
                  <Badge variant="arxiv">
                    arXiv:{cit.arxiv_id}
                  </Badge>
                </div>

                {/* The chip text carries the meaning; the tooltip only explains it (opens on hover and keyboard focus).
                    One chip per check: a passage cited by several claims shows each check (rows land in FE-4b). */}
                <div className="flex flex-wrap justify-end gap-1">
                  {entries.map(({ citation, pos }) => {
                    const check = citationState(citation, { refusal: !!refusalReason });
                    const ChipIcon = CHIP_ICONS[check.kind];
                    return (
                      <Tooltip key={pos}>
                        <TooltipTrigger asChild>
                          <button
                            type="button"
                            className="shrink-0 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card"
                          >
                            <Badge variant={check.kind} className="gap-1 text-[11px]">
                              <ChipIcon className="w-3 h-3" aria-hidden="true" />
                              <span>{check.text}</span>
                            </Badge>
                          </button>
                        </TooltipTrigger>
                        <TooltipContent>{check.tooltip}</TooltipContent>
                      </Tooltip>
                    );
                  })}
                </div>
              </div>

              <CardTitle className="text-sm font-semibold text-foreground mt-2 leading-snug">
                {cit.title}
              </CardTitle>
              <div className="text-xs text-muted-foreground mt-0.5">
                § Section: {cit.section || 'General'} • Chunk #{cit.chunk_id}
              </div>
            </CardHeader>

            <CardContent className="p-4 pt-1">
              {/* Exact Raw Text Quote */}
              <div className="text-sm font-serif text-foreground bg-muted/50 border-l-2 border-border rounded-r-md p-3 max-h-36 overflow-y-auto leading-relaxed whitespace-pre-wrap">
                {cit.content || `"${cit.title} — [Section: ${cit.section}]"`}
              </div>
            </CardContent>

            <CardFooter className="p-4 pt-0 border-t border-border/40 flex items-center justify-between gap-2 mt-2">
              <Button
                asChild
                variant="outline"
                size="sm"
                className="text-xs text-primary border-input hover:bg-accent hover:text-accent-foreground gap-1.5 h-7"
              >
                <a href={pdfURL} target="_blank" rel="noreferrer" aria-label={`Open PDF for [${cit.marker}] (opens in a new tab)`}>
                  <ExternalLink className="w-3 h-3" />
                  <span>Open PDF ↗</span>
                </a>
              </Button>

              <Button
                variant="ghost"
                size="sm"
                onClick={() => handleBibTeXCopy(cit)}
                aria-label={`Copy BibTeX for [${cit.marker}]`}
                className="text-xs text-muted-foreground hover:text-foreground gap-1.5 h-7"
              >
                <FileCode className="w-3 h-3" />
                <span>Copy BibTeX</span>
              </Button>
            </CardFooter>
          </Card>
        );
      })}
    </div>
  );
}
