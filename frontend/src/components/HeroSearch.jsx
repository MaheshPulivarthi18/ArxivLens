import React, { useState } from 'react';
import { Search, Sparkles, AlertCircle, Stethoscope, Cpu, Activity, Dna } from 'lucide-react';
import { Button } from './ui/button';
import { Badge } from './ui/badge';

const SUGGESTED_QUERIES = [
  {
    label: "G-CARL Medical Report Interpretation",
    query: "How does G-CARL improve patient-oriented medical report interpretation?",
    icon: Stethoscope,
    badge: "Clinical AI"
  },
  {
    label: "Task Model Goal Induction",
    query: "How does Task Model Induction recover goals from computer use activity traces?",
    icon: Cpu,
    badge: "cs.AI"
  },
  {
    label: "Autonomous Driving Multi-Agent LLMs",
    query: "Multi-agent orchestration for autonomous driving",
    icon: Activity,
    badge: "Robotics"
  },
  {
    label: "Test Early Refusal (Out-of-Domain)",
    query: "How to bake a chocolate sourdough cake?",
    icon: AlertCircle,
    badge: "Refusal Check"
  }
];

export default function HeroSearch({ onSearch, loading }) {
  const [query, setQuery] = useState(SUGGESTED_QUERIES[0].query);

  const handleSubmit = (e) => {
    e.preventDefault();
    if (query.trim()) {
      onSearch(query.trim());
    }
  };

  const handleChipClick = (q) => {
    setQuery(q);
    onSearch(q);
  };

  return (
    <div className="max-w-4xl mx-auto text-center my-8 sm:my-12">
      <div className="inline-flex items-center gap-2 px-3 py-1 rounded-full bg-emerald-500/10 border border-emerald-500/20 text-emerald-400 text-xs font-medium mb-4">
        <Sparkles className="w-3.5 h-3.5" />
        <span>Evidence-Grounded Synthesis with Sentence-Level Verification</span>
      </div>

      <h1 className="font-title font-bold text-3xl sm:text-4xl text-foreground tracking-tight mb-3">
        Query Peer-Reviewed Research Literature
      </h1>
      <p className="text-sm sm:text-base text-muted-foreground max-w-2xl mx-auto mb-6">
        Ask biomedical, AI, and clinical research questions. Every claim is mathematically verified against underlying PostgreSQL vector embeddings.
      </p>

      {/* Main Clinical Search Bar */}
      <form onSubmit={handleSubmit} className="relative max-w-3xl mx-auto">
        <div className="relative flex items-center bg-card border border-border/90 rounded-xl p-1.5 shadow-2xl shadow-black/40 focus-within:border-emerald-500 focus-within:ring-2 focus-within:ring-emerald-500/20 transition-all">
          <Search className="w-5 h-5 text-muted-foreground ml-3 shrink-0" />
          <input
            type="text"
            className="w-full bg-transparent px-3 py-2 text-sm sm:text-base text-foreground placeholder:text-muted-foreground outline-none"
            placeholder="Ask a clinical question, methodology, or benchmark..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <Button
            type="submit"
            variant="clinical"
            disabled={loading}
            className="shrink-0 gap-2 px-5 py-2.5 h-auto rounded-lg"
          >
            {loading ? (
              <>
                <div className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                <span>Synthesizing...</span>
              </>
            ) : (
              <>
                <span>Search Evidence</span>
              </>
            )}
          </Button>
        </div>
      </form>

      {/* Clinical Topic Chips */}
      <div className="flex flex-wrap items-center justify-center gap-2 mt-4 max-w-3xl mx-auto">
        <span className="text-xs text-muted-foreground mr-1 font-medium">Suggested Topics:</span>
        {SUGGESTED_QUERIES.map((item, idx) => {
          const Icon = item.icon;
          return (
            <button
              key={idx}
              type="button"
              onClick={() => handleChipClick(item.query)}
              className="inline-flex items-center gap-1.5 px-3 py-1 rounded-lg bg-card/80 border border-border hover:border-emerald-500/40 hover:bg-emerald-500/5 text-xs text-muted-foreground hover:text-foreground transition-all"
            >
              <Icon className="w-3 h-3 text-emerald-400" />
              <span>{item.label}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
