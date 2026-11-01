import React, { useState } from 'react';
import { DownloadCloud, CheckCircle2, AlertCircle, Loader2, FileText, ArrowRight, ExternalLink } from 'lucide-react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from './ui/dialog';
import { Button } from './ui/button';
import { Badge } from './ui/badge';

export default function IngestModal({ isOpen, onClose, onIngestSuccess, onAskAboutPaper }) {
  const [inputVal, setInputVal] = useState('2608.20154');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);
  const [result, setResult] = useState(null);
  const [step, setStep] = useState('');

  const handleIngest = async (e) => {
    e.preventDefault();
    if (!inputVal.trim()) return;

    setLoading(true);
    setError(null);
    setResult(null);
    setStep('Fetching metadata from arXiv Atom API...');

    try {
      setTimeout(() => {
        if (loading) setStep('Streaming PDF into MinIO S3 & extracting typography...');
      }, 1200);

      const resp = await fetch('/v1/papers/ingest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ arxiv_id: inputVal.trim() })
      });

      const data = await resp.json();

      if (!resp.ok || data.error) {
        throw new Error(data.error || 'Failed to ingest paper from arXiv');
      }

      setResult(data);
      if (onIngestSuccess) onIngestSuccess(data);
    } catch (err) {
      console.error("Ingestion error:", err);
      setError(err.message || 'Ingestion failed. Verify the arXiv ID.');
    } finally {
      setLoading(false);
    }
  };

  const handleQuickAsk = () => {
    if (result && result.title) {
      onAskAboutPaper(`What are the key contributions and methodology of "${result.title}"?`);
      onClose();
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={onClose}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <div className="p-1.5 rounded-lg bg-emerald-500/10 text-emerald-400">
              <DownloadCloud className="w-5 h-5" />
            </div>
            <div>
              <DialogTitle>Index New Research Paper On-Demand</DialogTitle>
              <DialogDescription className="text-xs">
                Paste any arXiv paper URL or ID to crawl, stream to MinIO, and GPU vector-embed into PostgreSQL.
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <form onSubmit={handleIngest} className="space-y-4 pt-2">
          <div>
            <label className="block text-xs font-semibold uppercase text-muted-foreground mb-1.5">
              arXiv Paper URL or ID:
            </label>
            <div className="flex gap-2">
              <input
                type="text"
                className="flex-1 bg-background border border-border rounded-lg px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground outline-none focus:border-emerald-500 font-mono"
                placeholder="e.g. 2608.20154 or https://arxiv.org/abs/2608.20154"
                value={inputVal}
                onChange={(e) => setInputVal(e.target.value)}
                disabled={loading}
              />
              <Button
                type="submit"
                variant="clinical"
                disabled={loading}
                className="gap-2 shrink-0"
              >
                {loading ? (
                  <>
                    <Loader2 className="w-4 h-4 animate-spin" />
                    <span>Indexing...</span>
                  </>
                ) : (
                  <>
                    <DownloadCloud className="w-4 h-4" />
                    <span>Index Paper</span>
                  </>
                )}
              </Button>
            </div>
            <p className="text-[11px] text-muted-foreground mt-1">
              Supports formats: <code>2608.20154</code>, <code>https://arxiv.org/abs/2608.20154</code>, or <code>https://arxiv.org/pdf/2608.20154.pdf</code>
            </p>
          </div>

          {loading && (
            <div className="p-4 rounded-lg bg-muted/40 border border-border text-center space-y-2">
              <Loader2 className="w-6 h-6 animate-spin text-emerald-400 mx-auto" />
              <div className="text-xs text-foreground font-medium">{step}</div>
              <p className="text-[11px] text-muted-foreground">Zero disk buffering • Streaming to MinIO S3 & GPU embedding</p>
            </div>
          )}

          {error && (
            <div className="flex items-center gap-2 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-300 text-xs">
              <AlertCircle className="w-4 h-4 shrink-0 text-red-400" />
              <span>{error}</span>
            </div>
          )}

          {result && (
            <div className="p-4 rounded-xl bg-emerald-500/10 border border-emerald-500/30 space-y-3 animate-in fade-in duration-200">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2 text-emerald-400 font-semibold text-xs">
                  <CheckCircle2 className="w-4 h-4" />
                  <span>Paper Successfully Ingested & Vector Indexed!</span>
                </div>
                <Badge variant="arxiv">arXiv:{result.arxiv_id}</Badge>
              </div>

              <div className="text-sm font-semibold text-foreground">
                {result.title}
              </div>

              <div className="grid grid-cols-3 gap-2 pt-1">
                <div className="bg-background/80 p-2 rounded border border-border/80 text-center">
                  <div className="text-[10px] text-muted-foreground uppercase">Sections</div>
                  <div className="text-base font-bold text-foreground font-title">{result.sections}</div>
                </div>
                <div className="bg-background/80 p-2 rounded border border-border/80 text-center">
                  <div className="text-[10px] text-muted-foreground uppercase">Chunks</div>
                  <div className="text-base font-bold text-foreground font-title">{result.chunks}</div>
                </div>
                <div className="bg-background/80 p-2 rounded border border-border/80 text-center">
                  <div className="text-[10px] text-muted-foreground uppercase">Embeddings</div>
                  <div className="text-base font-bold text-emerald-400 font-title">{result.embeddings}</div>
                </div>
              </div>

              <div className="pt-2 flex justify-end gap-2">
                <Button
                  type="button"
                  variant="clinical"
                  size="sm"
                  onClick={handleQuickAsk}
                  className="gap-1.5 text-xs"
                >
                  <span>Ask a question about this paper</span>
                  <ArrowRight className="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>
          )}
        </form>
      </DialogContent>
    </Dialog>
  );
}
