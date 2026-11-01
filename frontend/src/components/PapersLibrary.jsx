import React, { useEffect, useState } from 'react';
import { BookOpen, ExternalLink, ArrowLeft, Search, Tag, CheckCircle2 } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';

export default function PapersLibrary({ onBackToSearch }) {
  const [papers, setPapers] = useState([]);
  const [loading, setLoading] = useState(true);
  const [filterText, setFilterText] = useState('');

  useEffect(() => {
    fetch('/v1/stats')
      .then((res) => res.json())
      .then((data) => {
        if (data.papers) {
          setPapers(data.papers);
        }
        setLoading(false);
      })
      .catch((err) => {
        console.error("Failed to load papers library:", err);
        setLoading(false);
      });
  }, []);

  const filteredPapers = papers.filter((p) => {
    const term = filterText.toLowerCase();
    return (
      (p.Title && p.Title.toLowerCase().includes(term)) ||
      (p.ArxivID && p.ArxivID.toLowerCase().includes(term)) ||
      (p.Categories && p.Categories.some((c) => c.toLowerCase().includes(term)))
    );
  });

  return (
    <div className="max-w-6xl mx-auto my-8 space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-2xl font-title font-bold text-foreground">
            Indexed Clinical & AI Research Papers
          </h2>
          <p className="text-sm text-muted-foreground">
            {papers.length} peer-reviewed papers downloaded, typography chunked, and vector embedded.
          </p>
        </div>

        <div className="flex items-center gap-3">
          <div className="relative">
            <Search className="w-4 h-4 text-muted-foreground absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              className="bg-card border border-border rounded-lg pl-9 pr-3 py-1.5 text-xs sm:text-sm text-foreground placeholder:text-muted-foreground outline-none focus:border-emerald-500 w-48 sm:w-64"
              placeholder="Filter by title or ID..."
              value={filterText}
              onChange={(e) => setFilterText(e.target.value)}
            />
          </div>

          <Button
            variant="outline"
            size="sm"
            onClick={onBackToSearch}
            className="gap-2 shrink-0"
          >
            <ArrowLeft className="w-4 h-4" />
            <span>Back to Search</span>
          </Button>
        </div>
      </div>

      <Card className="border-border/80 bg-card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse text-sm">
            <thead>
              <tr className="border-b border-border/80 bg-muted/40 text-xs font-semibold uppercase text-muted-foreground">
                <th className="py-3 px-4">arXiv ID</th>
                <th className="py-3 px-4">Paper Title</th>
                <th className="py-3 px-4">Categories</th>
                <th className="py-3 px-4">Status</th>
                <th className="py-3 px-4 text-right">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border/40">
              {loading ? (
                <tr>
                  <td colSpan="5" className="py-12 text-center text-muted-foreground text-sm">
                    <div className="w-5 h-5 border-2 border-emerald-500 border-t-transparent rounded-full animate-spin mx-auto mb-2" />
                    <span>Loading indexed research catalog...</span>
                  </td>
                </tr>
              ) : filteredPapers.length === 0 ? (
                <tr>
                  <td colSpan="5" className="py-12 text-center text-muted-foreground text-sm">
                    No papers matching filter.
                  </td>
                </tr>
              ) : (
                filteredPapers.map((p) => (
                  <tr key={p.ID || p.ArxivID} className="hover:bg-muted/20 transition-colors">
                    <td className="py-3.5 px-4 font-mono text-xs text-muted-foreground whitespace-nowrap">
                      {p.ArxivID} <span className="text-muted-foreground/60">(v{p.Version})</span>
                    </td>
                    <td className="py-3.5 px-4 font-medium text-foreground max-w-md">
                      {p.Title}
                    </td>
                    <td className="py-3.5 px-4">
                      <div className="flex flex-wrap gap-1">
                        {(p.Categories || []).map((cat, cIdx) => (
                          <Badge key={cIdx} variant="arxiv" className="text-[10px]">
                            {cat}
                          </Badge>
                        ))}
                      </div>
                    </td>
                    <td className="py-3.5 px-4 whitespace-nowrap">
                      <Badge variant="clinical" className="gap-1 text-[11px]">
                        <CheckCircle2 className="w-3 h-3 text-emerald-400" />
                        <span>{p.Status}</span>
                      </Badge>
                    </td>
                    <td className="py-3.5 px-4 text-right whitespace-nowrap">
                      <Button
                        asChild
                        variant="ghost"
                        size="sm"
                        className="text-xs text-emerald-400 hover:text-emerald-300 hover:bg-emerald-500/10 gap-1.5 h-7"
                      >
                        <a href={`https://arxiv.org/pdf/${p.ArxivID}.pdf`} target="_blank" rel="noreferrer">
                          <span>View PDF</span>
                          <ExternalLink className="w-3 h-3" />
                        </a>
                      </Button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
