# Core seeds

`shared` creates the system admin and manager groups and is tagged `dev` and `prod`. It never creates users or project sites.

Project data is declared with `app.DatabaseDefinition.Seeds`; this application keeps its dev source in `internal/seeds`.
