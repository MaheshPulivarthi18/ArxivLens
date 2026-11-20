import React from 'react';
import { Microscope, BookOpen, Layers, Settings, DownloadCloud } from 'lucide-react';
import { Button } from './ui/button';
import ThemeToggle from './ThemeToggle';

// Visible keyboard focus in both themes (ring = accent, >= 3:1 on background).
const focusRing =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background';

export default function Navbar({ activeTab, setActiveTab, onOpenAdmin, onOpenIngest }) {
  const tabClass = (tab) =>
    `gap-2 ${focusRing} ${activeTab === tab ? 'bg-secondary font-semibold text-foreground' : 'text-muted-foreground'}`;

  return (
    <header className="sticky top-0 z-40 w-full border-b border-border/80 bg-background/90 backdrop-blur-md">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between">
        {/* Brand */}
        <button
          type="button"
          className={`flex items-center gap-3 rounded-md text-left ${focusRing}`}
          onClick={() => setActiveTab('search')}
        >
          <span className="w-10 h-10 rounded-md border border-border bg-card text-primary flex items-center justify-center">
            <Microscope className="w-5 h-5" aria-hidden="true" />
          </span>
          <span className="flex flex-col">
            <span className="font-serif font-semibold text-xl text-foreground tracking-tight">ArxivLens</span>
            <span className="text-xs text-muted-foreground">Precision-Grounded Literature Engine</span>
          </span>
        </button>

        {/* Center & Right Navigation */}
        <nav aria-label="Main" className="flex items-center gap-2 sm:gap-3">
          <Button
            variant={activeTab === 'search' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setActiveTab('search')}
            aria-current={activeTab === 'search' ? 'page' : undefined}
            className={tabClass('search')}
          >
            <BookOpen className="w-4 h-4" aria-hidden="true" />
            <span className="hidden sm:inline">Literature Search</span>
          </Button>

          <Button
            variant={activeTab === 'library' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setActiveTab('library')}
            aria-current={activeTab === 'library' ? 'page' : undefined}
            className={tabClass('library')}
          >
            <Layers className="w-4 h-4" aria-hidden="true" />
            <span className="hidden sm:inline">Research Library</span>
          </Button>

          <Button
            variant="default"
            size="sm"
            onClick={onOpenIngest}
            className={`gap-1.5 text-xs ${focusRing}`}
          >
            <DownloadCloud className="w-4 h-4" aria-hidden="true" />
            <span>Index Paper</span>
          </Button>

          <div className="h-5 w-[1px] bg-border mx-1" />

          <Button
            variant="outline"
            size="sm"
            onClick={onOpenAdmin}
            aria-label="Infrastructure & DLQ"
            className={`gap-2 text-muted-foreground hover:text-foreground border-input ${focusRing}`}
          >
            <Settings className="w-3.5 h-3.5" aria-hidden="true" />
            <span className="hidden md:inline">Infrastructure & DLQ</span>
          </Button>

          <ThemeToggle />
        </nav>
      </div>
    </header>
  );
}
