import React from 'react';
import { FileText, Copy, Download, ShieldCheck, Check, Sparkles } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent, CardFooter } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';

export default function SynthesisView({ answer, traceId, citations, onCitationClick, onCopy }) {
  // Parse answer text to render clickable [1], [2] citation badges
  const renderFormattedAnswer = (text) => {
    const parts = [];
    const regex = /\[(\d+)\]/g;
    let lastIndex = 0;
    let match;

    while ((match = regex.exec(text)) !== null) {
      if (match.index > lastIndex) {
        parts.push(text.substring(lastIndex, match.index));
      }
      const marker = parseInt(match[1], 10);
      parts.push(
        <button
          key={match.index}
          className="inline-flex items-center justify-center font-mono font-bold text-xs bg-emerald-500/15 border border-emerald-500/40 text-emerald-300 hover:bg-emerald-500 hover:text-slate-950 px-1.5 py-0.5 rounded mx-1 transition-all hover:scale-105 active:scale-95 cursor-pointer align-baseline"
          onClick={() => onCitationClick(marker)}
          title={`Click to inspect Evidence [${marker}]`}
        >
          [{marker}]
        </button>
      );
      lastIndex = regex.lastIndex;
    }

    if (lastIndex < text.length) {
      parts.push(text.substring(lastIndex));
    }

    return parts;
  };

  const handleCopySummary = () => {
    onCopy(answer, "📋 Clinical summary copied to clipboard!");
  };

  const handleExportMarkdown = () => {
    let md = `# Clinical & Academic Research Note\n\n${answer}\n\n## References & Verified Citations\n`;
    (citations || []).forEach((c) => {
      const pdf = c.pdf_url || `https://arxiv.org/pdf/${c.arxiv_id}.pdf`;
      const score = (c.score * 100).toFixed(1);
      md += `- [${c.marker}] **${c.title}** (§ ${c.section}) - [arXiv:${c.arxiv_id}](${pdf}) • Support: ${score}%\n`;
    });
    onCopy(md, "📥 Markdown clinical research note copied to clipboard!");
  };

  return (
    <Card className="border-border/80 bg-card/95 shadow-xl relative overflow-hidden">
      <div className="absolute top-0 left-0 right-0 h-1 bg-gradient-to-r from-emerald-500 via-teal-500 to-cyan-500" />
      
      <CardHeader className="pb-3 border-b border-border/60 flex flex-row items-center justify-between space-y-0">
        <div className="flex items-center gap-2.5">
          <div className="p-1.5 rounded-lg bg-emerald-500/10 text-emerald-400">
            <FileText className="w-4 h-4" />
          </div>
          <div>
            <CardTitle className="text-base font-title text-foreground">
              Clinical Literature Synthesis
            </CardTitle>
            <p className="text-xs text-muted-foreground">Multi-paper grounded answer</p>
          </div>
        </div>

        <Badge variant="arxiv" className="font-mono">
          Trace: {traceId ? traceId.substring(0, 12) + '...' : 'live'}
        </Badge>
      </CardHeader>

      <CardContent className="pt-5 text-sm sm:text-base leading-relaxed text-slate-200 font-sans">
        {renderFormattedAnswer(answer)}
      </CardContent>

      <CardFooter className="pt-2 pb-4 border-t border-border/40 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={handleCopySummary}
            className="gap-1.5 text-xs text-muted-foreground hover:text-foreground"
          >
            <Copy className="w-3.5 h-3.5" />
            <span>Copy Synthesis</span>
          </Button>

          <Button
            variant="outline"
            size="sm"
            onClick={handleExportMarkdown}
            className="gap-1.5 text-xs text-muted-foreground hover:text-foreground"
          >
            <Download className="w-3.5 h-3.5" />
            <span>Export Note</span>
          </Button>
        </div>

        <div className="flex items-center gap-1.5 text-xs text-emerald-400 font-medium">
          <ShieldCheck className="w-4 h-4" />
          <span>PostgreSQL pgvector Verified</span>
        </div>
      </CardFooter>
    </Card>
  );
}
