import React from 'react';
import { ExternalLink, FileCode, ShieldCheck, ShieldAlert, Info, BookOpen } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent, CardFooter } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';
import { citationState } from '../lib/citations';

const CHIP_ICONS = { supported: ShieldCheck, unsupported: ShieldAlert, unchecked: Info };

export default function ReferencesStack({ citations, activeMarker, onCopy, refusalReason }) {
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
            {citations.length} {refusalReason ? (citations.length === 1 ? 'passage' : 'passages') : 'cited'}
          </Badge>
        </h3>
        {!refusalReason && <span className="text-xs text-muted-foreground">Click [n] in text to inspect</span>}
      </div>

      {citations.map((cit) => {
        const isHighlighted = activeMarker === cit.marker;
        const pdfURL = cit.pdf_url || `https://arxiv.org/pdf/${cit.arxiv_id}.pdf`;
        const check = citationState(cit, { refusal: !!refusalReason });
        const ChipIcon = CHIP_ICONS[check.kind];

        return (
          <Card
            key={cit.marker}
            id={`ref-${cit.marker}`}
            role="article"
            aria-label={`Citation [${cit.marker}]: ${cit.title}`}
            className={`transition-all duration-300 ${
              isHighlighted
                ? 'border-emerald-500 ring-2 ring-emerald-500/20 bg-card shadow-lg shadow-emerald-950/20'
                : 'border-border/70 hover:border-border bg-card/90'
            }`}
          >
            <CardHeader className="p-4 pb-2">
              <div className="flex items-start justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span className="flex items-center justify-center w-6 h-6 rounded-md bg-emerald-600 text-slate-950 font-mono font-bold text-xs">
                    [{cit.marker}]
                  </span>
                  <Badge variant="arxiv">
                    arXiv:{cit.arxiv_id}
                  </Badge>
                </div>

                <Badge variant={check.kind} className="gap-1 text-[11px]" title={check.tooltip}>
                  <ChipIcon className="w-3 h-3" aria-hidden="true" />
                  <span>{check.text}</span>
                </Badge>
              </div>

              <CardTitle className="text-sm font-semibold text-foreground mt-2 leading-snug">
                {cit.title}
              </CardTitle>
              <div className="text-xs text-emerald-400 font-mono mt-0.5">
                § Section: {cit.section || 'General'} • Chunk #{cit.chunk_id}
              </div>
            </CardHeader>

            <CardContent className="p-4 pt-1">
              {/* Exact Raw Text Quote */}
              <div className="text-xs font-mono text-slate-300 bg-background/80 border-l-2 border-emerald-500 rounded-r-md p-3 max-h-36 overflow-y-auto leading-relaxed whitespace-pre-wrap">
                {cit.content || `"${cit.title} — [Section: ${cit.section}]"`}
              </div>
            </CardContent>

            <CardFooter className="p-4 pt-0 border-t border-border/40 flex items-center justify-between gap-2 mt-2">
              <Button
                asChild
                variant="outline"
                size="sm"
                className="text-xs text-emerald-400 hover:text-emerald-300 border-emerald-500/30 hover:bg-emerald-500/10 gap-1.5 h-7"
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
